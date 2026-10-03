// Pinned file-transfer connections, framed streaming, and integrity checks.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"time"

	"noxa/internal/netproto"
	"noxa/internal/tlscert"
)

// ftFrame types of the file-transfer port protocol (internal/filetransfer).
const (
	ftInit   uint16 = 1
	ftChunk  uint16 = 2
	ftDigest uint16 = 3
	ftStatus uint16 = 4
)

// ftDial dials the file-transfer port. The data port is TLS with the same
// certificate as the control channel, pinned here: file bytes must not cross
// the network in the clear just because they use a different port (91-135).
// ftTarget only clears ep.tls for an all-plaintext dev server.
func ftDial(ep ftEndpoint) (net.Conn, error) {
	if !ep.tls {
		return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(context.Background(), "tcp", ep.addr)
	}
	if ep.fingerprint == "" {
		return nil, errors.New("file transfer TLS fingerprint is missing")
	}
	tlsConfig, err := pinnedTLSConfig(ep.certDER, ep.fingerprint)
	if err != nil {
		return nil, err
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 15 * time.Second},
		Config:    tlsConfig,
	}
	return dialer.DialContext(context.Background(), "tcp", ep.addr)
}

// pinnedTLSConfig turns the exact certificate authenticated by the control
// channel into a private trust store for the data port. Standard certificate
// verification therefore remains enabled, while VerifyConnection keeps the
// fingerprint pin explicit as a defense-in-depth check.
func pinnedTLSConfig(certDER []byte, fingerprint string) (*tls.Config, error) {
	if fingerprint == "" {
		return nil, errors.New("file transfer TLS fingerprint is missing")
	}
	if len(certDER) == 0 {
		return nil, errors.New("file transfer TLS certificate is missing")
	}
	leaf, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parsing file transfer TLS certificate: %w", err)
	}
	if got := tlscert.FingerprintDER(leaf.Raw); !secureEqualFold(got, fingerprint) {
		return nil, fmt.Errorf("file transfer certificate mismatch (%s, expected %s)", got, fingerprint)
	}

	serverName := ""
	if len(leaf.DNSNames) > 0 {
		serverName = leaf.DNSNames[0]
	} else if len(leaf.IPAddresses) > 0 {
		serverName = leaf.IPAddresses[0].String()
	}
	if serverName == "" {
		return nil, errors.New("file transfer TLS certificate has no DNS or IP subject alternative name")
	}

	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return &tls.Config{
		MinVersion:       tls.VersionTLS13,
		RootCAs:          roots,
		ServerName:       serverName,
		VerifyConnection: pinFingerprint(fingerprint),
	}, nil
}

// transferDial is injectable for deterministic disconnect-between-endpoint-
// and-dial tests. Production always uses ftDial.
var transferDial = ftDial

// pinFingerprint builds the mandatory certificate check for the data port.
// An empty pin fails closed because server-generated self-signed certificates
// have no PKI trust anchor; the established control-channel pin is that anchor.
func pinFingerprint(want string) func(tls.ConnectionState) error {
	if want == "" {
		return func(tls.ConnectionState) error {
			return errors.New("file transfer TLS fingerprint is missing")
		}
	}
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("file transfer server presented no certificate")
		}
		got := tlscert.FingerprintDER(state.PeerCertificates[0].Raw)
		if !secureEqualFold(got, want) {
			return fmt.Errorf("file transfer certificate mismatch (%s, expected %s)", got, want)
		}
		return nil
	}
}

// ftUpload streams data to the file-transfer port with digest verification.
func ftUpload(ep ftEndpoint, token, transferID string, data []byte) error {
	conn, err := transferDial(ep)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return ftUploadConn(conn, token, transferID, data)
}

// ftUpload keeps even synchronous transfer paths bound to the captured
// connection epoch. A disconnect after ftTarget but before dial is rejected
// and the freshly dialed socket is closed before protocol bytes are written.
func (m *connManager) ftUpload(ep ftEndpoint, token, transferID string, data []byte) error {
	conn, err := transferDial(ep)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	untrack, ok := m.trackTransferAt(transferID, ep.epoch, conn)
	if !ok {
		return errTransferCanceled
	}
	defer untrack()
	return ftUploadConn(conn, token, transferID, data)
}

func ftUploadConn(conn net.Conn, token, transferID string, data []byte) error {
	defer clearTransferDeadlines(conn)
	if err := ftWriteJSON(conn, ftInit, map[string]string{"token": token, "transfer_id": transferID}); err != nil {
		return err
	}
	const chunk = 32 * 1024
	for off := 0; off < len(data); off += chunk {
		end := off + chunk
		if end > len(data) {
			end = len(data)
		}
		if err := ftWriteFrame(conn, &netproto.Frame{Type: ftChunk, Payload: data[off:end]}); err != nil {
			return err
		}
	}
	sum := sha256.Sum256(data)
	if err := ftWriteJSON(conn, ftDigest, map[string]string{"sha256": hex.EncodeToString(sum[:])}); err != nil {
		return err
	}
	return ftReadStatus(conn)
}

func (m *connManager) ftDownload(ep ftEndpoint, token, transferID string) ([]byte, error) {
	return m.ftDownloadWithLimit(ep, token, transferID, maxLegacyTransferBytes)
}

func (m *connManager) ftDownloadWithLimit(ep ftEndpoint, token, transferID string, maxBytes int64) ([]byte, error) {
	conn, err := transferDial(ep)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	untrack, ok := m.trackTransferAt(transferID, ep.epoch, conn)
	if !ok {
		return nil, errTransferCanceled
	}
	defer untrack()
	return ftDownloadConnWithLimit(conn, token, transferID, maxBytes)
}

const maxLegacyTransferBytes = 25 << 20

func ftDownloadConnWithLimit(conn net.Conn, token, transferID string, maxBytes int64) ([]byte, error) {
	var out bytes.Buffer
	if _, err := ftDownloadTo(conn, token, transferID, &out, maxBytes); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ftDownloadTo is the shared streaming download core. maxBytes <= 0 permits
// unbounded streaming (the progress-to-disk route); legacy in-memory callers
// are capped before a chunk is written.
func ftDownloadTo(conn net.Conn, token, transferID string, out io.Writer, maxBytes int64) (int64, error) {
	return ftDownloadStream(
		conn,
		map[string]string{"token": token, "transfer_id": transferID},
		out,
		maxBytes,
		0,
		sha256.New(),
		nil,
	)
}

// ftDownloadStream is the one streaming download protocol implementation.
// Callers may seed the hasher and total for a resumed download, and observe
// each verified-on-arrival chunk for progress reporting.
func ftDownloadStream(conn net.Conn, init any, out io.Writer, maxBytes, total int64, h hash.Hash, onChunk func(int64) error) (int64, error) {
	defer clearTransferDeadlines(conn)
	if err := ftWriteJSON(conn, ftInit, init); err != nil {
		return total, err
	}

	for {
		f, err := ftReadFrame(conn)
		if err != nil {
			return total, err
		}
		switch f.Type {
		case ftChunk:
			if maxBytes > 0 && total+int64(len(f.Payload)) > maxBytes {
				return total, fmt.Errorf("file exceeds %d byte in-memory transfer limit; use streaming download", maxBytes)
			}
			n, err := out.Write(f.Payload)
			if err != nil {
				return total, err
			}
			if n != len(f.Payload) {
				return total, io.ErrShortWrite
			}
			total += int64(len(f.Payload))
			h.Write(f.Payload)
			if onChunk != nil {
				if err := onChunk(total); err != nil {
					return total, err
				}
			}
		case ftStatus:
			var status struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(f.Payload, &status); err != nil {
				return total, err
			}
			if status.OK {
				return total, errors.New("download completed without a digest")
			}
			return total, fmt.Errorf("%w: %s", errFileTransferRejected, status.Error)
		case ftDigest:
			var d struct {
				SHA256 string `json:"sha256"`
			}
			if err := json.Unmarshal(f.Payload, &d); err != nil {
				return total, err
			}
			if d.SHA256 != hex.EncodeToString(h.Sum(nil)) {
				return total, errFileDigestMismatch
			}
			if err := ftReadStatus(conn); err != nil {
				return total, err
			}
			return total, nil
		default:
			return total, fmt.Errorf("unexpected frame type %d", f.Type)
		}
	}
}

var errFileDigestMismatch = errors.New("file digest mismatch")

var errFileTransferRejected = errors.New("transfer rejected")

var fileTransferIdleTimeout = 30 * time.Second

// ftWriteFrame and ftReadFrame use rolling idle deadlines: each individual
// frame must make progress within the timeout, while an active large transfer
// may run indefinitely. The enclosing transfer clears both deadlines on exit.
func ftWriteFrame(conn net.Conn, frame *netproto.Frame) error {
	_ = conn.SetWriteDeadline(time.Now().Add(fileTransferIdleTimeout))
	return netproto.WriteFrame(conn, frame)
}

func ftReadFrame(conn net.Conn) (*netproto.Frame, error) {
	_ = conn.SetReadDeadline(time.Now().Add(fileTransferIdleTimeout))
	return netproto.ReadFrame(conn)
}

func clearTransferDeadlines(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.SetWriteDeadline(time.Time{})
}

// ftWriteJSON writes a JSON payload frame on the file-transfer port.
func ftWriteJSON(conn net.Conn, frameType uint16, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return ftWriteFrame(conn, &netproto.Frame{Type: frameType, Payload: payload})
}

// ftReadStatus reads the server's final status frame.
func ftReadStatus(conn net.Conn) error {
	f, err := ftReadFrame(conn)
	if err != nil {
		return err
	}
	if f.Type != ftStatus {
		return fmt.Errorf("expected status frame, got %d", f.Type)
	}
	var st struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(f.Payload, &st); err != nil {
		return err
	}
	if !st.OK {
		return errors.New("transfer failed: " + st.Error)
	}
	return nil
}
