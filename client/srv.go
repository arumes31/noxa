package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const defaultControlPort = "12333"

type srvLookup func(context.Context, string, string, string) (string, []*net.SRV, error)

// serverAddress supplies the default port without DNS. The second result is
// nonempty only for a hostname whose port should be discovered through SRV.
func serverAddress(addr string) (string, string, error) {
	addr = strings.TrimSpace(addr)
	if _, _, err := net.SplitHostPort(addr); err == nil {
		_, err = normalizeServerAddr(addr)
		return addr, "", err
	}
	host := addr
	if strings.HasPrefix(addr, "[") && strings.HasSuffix(addr, "]") {
		host = addr[1 : len(addr)-1]
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return net.JoinHostPort(host, defaultControlPort), "", nil
	}
	if addr == "" || addr == "." || strings.ContainsAny(addr, " :/\\?#[]@\t\r\n") {
		return "", "", errors.New("server address must be a hostname or IP, optionally with a port")
	}
	return net.JoinHostPort(addr, defaultControlPort), addr, nil
}

// resolveServerAddresses preserves LookupSRV's priority/weight ordering.
// The trust address always describes the user's original destination; DNS
// changes must not turn an existing certificate pin into a first connection.
func resolveServerAddresses(ctx context.Context, addr string, lookup srvLookup) ([]string, string, error) {
	trustAddr, host, err := serverAddress(addr)
	if err != nil {
		return nil, "", err
	}
	fallback := []string{trustAddr}
	if host == "" {
		return fallback, trustAddr, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, records, err := lookup(ctx, "voicx", "tcp", host)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return fallback, trustAddr, nil
		}
		return nil, "", fmt.Errorf("resolve _voicx._tcp.%s: %w", host, err)
	}
	if len(records) == 0 {
		return fallback, trustAddr, nil
	}
	addresses := make([]string, 0, len(records))
	for _, record := range records {
		if record != nil && record.Target == "." {
			return nil, "", fmt.Errorf("voicx service is unavailable for %s", host)
		}
		if record == nil || record.Port == 0 || record.Target == "" {
			return nil, "", fmt.Errorf("invalid voicx SRV record for %s", host)
		}
		target := strings.TrimSuffix(record.Target, ".")
		addresses = append(addresses, net.JoinHostPort(target, strconv.Itoa(int(record.Port))))
	}
	return addresses, trustAddr, nil
}

func (m *connManager) dialTransport(addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	lookup := m.lookupSRV
	if lookup == nil {
		lookup = net.DefaultResolver.LookupSRV
	}
	addresses, trustAddr, err := resolveServerAddresses(ctx, addr, lookup)
	if err != nil {
		return nil, err
	}
	for _, target := range addresses {
		var conn net.Conn
		conn, err = m.dialEndpoint(ctx, target, trustAddr)
		if err == nil {
			return conn, nil
		}
		// Trust failures are authoritative, even if another SRV target is up.
		if errors.Is(err, errFingerprintMismatch) || errors.Is(err, errTrustStoreUnavailable) || ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, err
}
