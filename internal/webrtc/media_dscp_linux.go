//go:build linux

package webrtc

import (
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// voiceTOS is DSCP EF (46) in the IPv4 TOS byte. Wi-Fi WMM maps it to the
// voice access category and Linux pfifo_fast maps it to the interactive band.
// Only Opus RTP is marked; video, RTCP, STUN and DTLS keep the default class.
const voiceTOS = 46 << 2

// mediaVoiceDSCP can be turned off if a deployment's network mishandles EF.
var mediaVoiceDSCP atomic.Bool

func init() { mediaVoiceDSCP.Store(true) }

var voiceTOSControl = func() []byte {
	b := make([]byte, syscall.CmsgSpace(4))
	h := (*syscall.Cmsghdr)(unsafe.Pointer(&b[0]))
	h.Level = syscall.IPPROTO_IP
	h.Type = syscall.IP_TOS
	h.SetLen(syscall.CmsgLen(4))
	*(*int32)(unsafe.Pointer(&b[syscall.CmsgLen(0)])) = voiceTOS
	return b
}()

// voiceDSCP remembers that per-packet marking failed on a socket so voice is
// never put at risk by marking: any non-timeout failure disables it and the
// packet is resent unmarked by the caller.
type voiceDSCP struct{ disabled atomic.Bool }

func writeVoiceMarked(d *voiceDSCP, conn net.PacketConn, b []byte, addr net.Addr) (int, bool, error) {
	if !mediaVoiceDSCP.Load() || d.disabled.Load() {
		return 0, false, nil
	}
	udp, ok := conn.(*net.UDPConn)
	dst, isUDP := addr.(*net.UDPAddr)
	if !ok || !isUDP || dst.IP.To4() == nil {
		return 0, false, nil
	}
	local, ok := udp.LocalAddr().(*net.UDPAddr)
	if !ok || (local.IP != nil && !local.IP.IsUnspecified() && local.IP.To4() == nil) {
		return 0, false, nil
	}
	n, _, err := udp.WriteMsgUDP(b, voiceTOSControl, dst)
	var timeout net.Error
	if err != nil && !(errors.As(err, &timeout) && timeout.Timeout()) {
		d.disabled.Store(true)
		return 0, false, nil
	}
	return n, true, err
}
