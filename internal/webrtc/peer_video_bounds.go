package webrtc

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pion/webrtc/v4"
)

// Pion derives answer codec preferences from the remote offer. Restore our
// receiver's bounds before creating SDP so an offer without max-fs cannot
// suppress the server's advertised preference. Packet inspection remains the
// authoritative per-axis limit; max-fs only describes a macroblock count.
func (w *PeerConnectionWrapper) setVideoBoundsCodecPreferences() error {
	macroblocks := ((w.videoBounds.Width + 15) / 16) * ((w.videoBounds.Height + 15) / 16)
	// Pion installs the offerer's order when creating a transceiver, but an
	// existing transceiver retains the previous preference. Honor new offers
	// as well, so a single sender can switch between its negotiated codecs.
	orders := make(map[string]map[string]int)
	if remote := w.pc.RemoteDescription(); remote != nil && w.pc.SignalingState() == webrtc.SignalingStateHaveRemoteOffer {
		// Unmarshal replaces the description's cached parse. Pion still reads
		// its own description from asynchronous transport setup, so parse a
		// private snapshot instead of mutating that shared object.
		snapshot := webrtc.SessionDescription{Type: remote.Type, SDP: remote.SDP}
		parsed, err := snapshot.Unmarshal()
		if err != nil {
			return fmt.Errorf("reading offered video codecs: %w", err)
		}
		for _, media := range parsed.MediaDescriptions {
			if media.MediaName.Media != "video" {
				continue
			}
			mid, _ := media.Attribute("mid")
			order := make(map[string]int)
			for i, payload := range media.MediaName.Formats {
				order[payload] = i + 1
			}
			orders[mid] = order
		}
	}
	for _, transceiver := range w.pc.GetTransceivers() {
		if transceiver.Kind() != webrtc.RTPCodecTypeVideo || transceiver.Receiver() == nil {
			continue
		}
		// Pion may return its MediaEngine's shared slice for transceivers with
		// no explicit preferences. Own the entries before changing any field.
		codecs := slices.Clone(transceiver.Receiver().GetParameters().Codecs)
		if order := orders[transceiver.Mid()]; order != nil {
			rank := func(codec webrtc.RTPCodecParameters) int {
				if value := order[strconv.Itoa(int(codec.PayloadType))]; value != 0 {
					return value
				}
				return len(order) + 1
			}
			slices.SortStableFunc(codecs, func(a, b webrtc.RTPCodecParameters) int { return rank(a) - rank(b) })
		}
		if w.videoBounds != (VideoBounds{}) {
			vp8Payloads := make(map[string]bool)
			for _, codec := range codecs {
				if strings.EqualFold(codec.MimeType, webrtc.MimeTypeVP8) {
					vp8Payloads[strconv.Itoa(int(codec.PayloadType))] = true
				}
			}
			codecs = slices.DeleteFunc(codecs, func(codec webrtc.RTPCodecParameters) bool {
				if strings.EqualFold(codec.MimeType, webrtc.MimeTypeVP8) {
					return false
				}
				// Admit repair only for a retained VP8 payload. Repair must not
				// reintroduce another codec into a dimension-bounded receiver.
				if strings.EqualFold(codec.MimeType, webrtc.MimeTypeRTX) {
					for _, parameter := range strings.Split(codec.SDPFmtpLine, ";") {
						key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
						if ok && key == "apt" {
							return !vp8Payloads[value]
						}
					}
				}
				return true
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
