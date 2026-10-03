// File-transfer target resolution and control-channel initialization.
package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"time"

	"noxa/internal/netproto"
)

// ftEndpoint is a resolved data-port target: where to dial, whether the port
// speaks TLS, and the certificate to pin on it.
type ftEndpoint struct {
	addr        string
	fingerprint string
	certDER     []byte
	tls         bool
	epoch       uint64
}

// ftTarget resolves the data-port address and the certificate to pin on it.
// The data port presents the SAME certificate as the control channel, so the
// pin the client already trusts is the pin here; a server that states a
// different one is redirecting the transfer to a host we never authenticated,
// and the transfer is refused rather than downgraded (91-135). A server that
// reports a plaintext data port is honoured only when its control channel is
// plaintext too, so a TLS server can never talk a client down to a clear port.
func (m *connManager) ftTarget(init netproto.FileTransferInitResponse) (ftEndpoint, error) {
	// The endpoint and epoch come from one manager snapshot. Reading the
	// address and certificate under separate locks could pair an old host with
	// a new connection's pin after reconnect.
	m.mu.Lock()
	conn := m.conn
	accepting := m.acceptingTransfers
	control := m.fingerprint
	controlCertDER := bytes.Clone(m.peerCertificateDER)
	epoch := m.transferEpoch
	m.mu.Unlock()
	if conn == nil || !accepting {
		return ftEndpoint{}, errors.New("not connected")
	}
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return ftEndpoint{}, err
	}
	addr := net.JoinHostPort(host, fmt.Sprint(init.Port))
	if !init.TLS {
		if control != "" {
			return ftEndpoint{}, errors.New("server offered a plaintext file transfer port — refusing the transfer")
		}
		return ftEndpoint{addr: addr, epoch: epoch}, nil
	}
	fingerprint := init.TLSFingerprint
	if fingerprint == "" {
		fingerprint = control // server did not state it; same certificate anyway
	}
	if fingerprint == "" {
		return ftEndpoint{}, errors.New("file transfer TLS fingerprint is missing — refusing the transfer")
	}
	if control != "" && !secureEqualFold(fingerprint, control) {
		return ftEndpoint{}, errors.New("file transfer certificate does not match the server — refusing the transfer")
	}
	if len(controlCertDER) == 0 {
		return ftEndpoint{}, errors.New("file transfer TLS certificate is unavailable — refusing the transfer")
	}
	return ftEndpoint{
		addr:        addr,
		fingerprint: fingerprint,
		certDER:     controlCertDER,
		tls:         true,
		epoch:       epoch,
	}, nil
}

// ftTarget resolves a transfer target for the manager active at call start.
// Long-running transfers call the connManager method directly so switching
// tabs cannot redirect a later stage of the same transfer.
func (a *App) ftTarget(init netproto.FileTransferInitResponse) (ftEndpoint, error) {
	cm, err := a.requireCM()
	if err != nil {
		return ftEndpoint{}, err
	}
	return cm.ftTarget(init)
}

// ftPutBytes runs the init handshake and streams data to the data port.
func (a *App) ftPutBytes(channelID int64, name string, data []byte) error {
	cm, err := a.requireCM()
	if err != nil {
		return err
	}
	return ftPutBytesWith(cm, channelID, name, data)
}

func ftPutBytesWith(cm *connManager, channelID int64, name string, data []byte) error {
	f, err := cm.request(netproto.MsgFileTransferInit, netproto.MsgFileTransferInitResponse,
		netproto.FileTransferInit{ChannelID: channelID, Direction: "upload", Name: name, Size: int64(len(data))},
		10*time.Second)
	if err != nil {
		return err
	}
	var init netproto.FileTransferInitResponse
	if err := decodeJSON(f, &init); err != nil {
		return err
	}
	ep, err := cm.ftTarget(init)
	if err != nil {
		return err
	}
	return cm.ftUpload(ep, init.Token, init.TransferID, data)
}

// ftGetBytes runs the init handshake and reads a file off the data port.
func (a *App) ftGetBytes(channelID int64, name string) ([]byte, error) {
	cm, err := a.requireCM()
	if err != nil {
		return nil, err
	}
	return a.ftGetBytesForCM(cm, channelID, name)
}

func (a *App) ftGetBytesForCM(cm *connManager, channelID int64, name string) ([]byte, error) {
	return a.ftGetBytesForCMWithLimit(cm, channelID, name, maxLegacyTransferBytes)
}

func (a *App) ftGetBytesForCMWithLimit(cm *connManager, channelID int64, name string, maxBytes int64) ([]byte, error) {
	f, err := cm.request(netproto.MsgFileTransferInit, netproto.MsgFileTransferInitResponse,
		netproto.FileTransferInit{ChannelID: channelID, Direction: "download", Name: name},
		10*time.Second)
	if err != nil {
		return nil, err
	}
	var init netproto.FileTransferInitResponse
	if err := decodeJSON(f, &init); err != nil {
		return nil, err
	}
	ep, err := cm.ftTarget(init)
	if err != nil {
		return nil, err
	}
	return cm.ftDownloadWithLimit(ep, init.Token, init.TransferID, maxBytes)
}

// UploadFile uploads data as a file into a channel and returns "" or the
// error. It is the PLAIN path, used by the file browser; chat attachments go
// through UploadChatAttachment instead.
func (a *App) UploadFile(channelID int64, name, dataBase64 string) string {
	data, err := base64.StdEncoding.DecodeString(dataBase64)
	if err != nil {
		return "invalid file data"
	}
	if err := a.ftPutBytes(channelID, name, data); err != nil {
		return err.Error()
	}
	return ""
}

// DownloadFile downloads a channel file, returned base64-encoded.
func (a *App) DownloadFile(channelID int64, name string) (string, error) {
	data, err := a.ftGetBytes(channelID, name)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}
