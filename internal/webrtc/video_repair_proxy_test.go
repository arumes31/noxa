//go:build integration

package webrtc

import (
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	pion "github.com/pion/webrtc/v4"
)

// Drop one encrypted media datagram after pacing and SRTP. The clear RTP
// header identifies the stream; this test never decrypts or rewrites media.
type videoRepairProxy struct {
	conn       *net.UDPConn
	upstream   *net.UDPAddr
	dropSSRC   atomic.Uint32
	repairSSRC atomic.Uint32
	dropped    atomic.Uint32
	repaired   atomic.Uint32
}

func newVideoRepairProxy(t *testing.T, upstream net.Addr) *videoRepairProxy {
	t.Helper()
	address, err := net.ResolveUDPAddr("udp4", upstream.String())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	p := &videoRepairProxy{conn: conn, upstream: address}
	done := make(chan struct{})
	t.Cleanup(func() { _ = conn.Close(); <-done })
	go func() {
		defer close(done)
		var browser *net.UDPAddr
		buffer := make([]byte, 65535)
		for {
			n, from, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			to := address
			if from.String() == address.String() {
				if browser == nil {
					continue
				}
				to = browser
			} else {
				browser = from
			}
			if n >= 12 && buffer[0]>>6 == 2 && (buffer[1] < 192 || buffer[1] > 223) {
				ssrc := binary.BigEndian.Uint32(buffer[8:12])
				if ssrc == p.repairSSRC.Load() {
					p.repaired.Add(1)
				}
				if n > 300 && buffer[0]&0x20 == 0 && ssrc != 0 && p.dropSSRC.CompareAndSwap(ssrc, 0) {
					p.dropped.Add(1)
					continue
				}
			}
			if _, err := conn.WriteToUDP(buffer[:n], to); err != nil {
				return
			}
		}
	}()
	return p
}

func (p *videoRepairProxy) description(original *pion.SessionDescription) *pion.SessionDescription {
	copy := *original
	// The test engine advertises exactly one loopback UDP host candidate.
	copy.SDP = strings.ReplaceAll(copy.SDP, " 127.0.0.1 "+strconv.Itoa(p.upstream.Port)+" typ host", " 127.0.0.1 "+strconv.Itoa(p.conn.LocalAddr().(*net.UDPAddr).Port)+" typ host")
	return &copy
}
