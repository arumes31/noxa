package webrtc

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pion/webrtc/v4"
)

// Pion derives answer codec preferences from the remote offer. Restore our
// receiver's bounds before creating SDP so an offer without max-fs cannot
// suppress the server's advertised preference. Packet inspection remains the
// authoritative per-axis limit; max-fs only describes a macroblock count.
func (w *PeerConnectionWrapper) setVideoBoundsCodecPreferences() error {
	macroblocks := ((w.videoBounds.Width + 15) / 16) * ((w.videoBounds.Height + 15) / 16)
	for _, transceiver := range w.pc.GetTransceivers() {
		if transceiver.Kind() != webrtc.RTPCodecTypeVideo || transceiver.Receiver() == nil {
			continue
		}
		// Pion may return its MediaEngine's shared slice for transceivers with
		// no explicit preferences. Own the entries before changing any field.
		codecs := slices.Clone(transceiver.Receiver().GetParameters().Codecs)
		if w.videoBounds != (VideoBounds{}) {
			codecs = slices.DeleteFunc(codecs, func(codec webrtc.RTPCodecParameters) bool {
				return !strings.EqualFold(codec.MimeType, webrtc.MimeTypeVP8)
			})
		}
		for i := range codecs {
			if strings.EqualFold(codecs[i].MimeType, webrtc.MimeTypeVP8) {
				codecs[i].SDPFmtpLine = ""
				if macroblocks > 0 {
					codecs[i].SDPFmtpLine = fmt.Sprintf("max-fs=%d", macroblocks)
				}
			}
		}
		if err := transceiver.SetCodecPreferences(codecs); err != nil {
			return fmt.Errorf("setting video receiver bounds: %w", err)
		}
	}
	return nil
}
