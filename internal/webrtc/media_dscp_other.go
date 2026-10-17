//go:build !linux

package webrtc

import "net"

type voiceDSCP struct{}

// Per-packet DSCP needs IP_TOS control messages; other platforms send unmarked.
func writeVoiceMarked(*voiceDSCP, net.PacketConn, []byte, net.Addr) (int, bool, error) {
	return 0, false, nil
}
