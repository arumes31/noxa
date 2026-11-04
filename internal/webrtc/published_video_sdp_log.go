package webrtc

import (
	"strconv"
	"strings"

	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
	"go.uber.org/zap"
)

// maxLoggedVideoFormats bounds the per-section codec list in negotiation logs.
// Browsers offer around a dozen video payloads including RTX/RED/FEC.
const maxLoggedVideoFormats = 24

// publishedVideoSection summarizes one client-published video m-section and
// the server's answer for it. It carries only media parameters: ICE
// credentials, DTLS fingerprints and candidates are never included.
type publishedVideoSection struct {
	mid             string
	trackID         string
	offerDirection  string
	offerFormats    []string
	answerPort      int
	answerDirection string
	answerFormats   []string
	answered        bool
}

func (s publishedVideoSection) rejected() bool {
	if !s.answered || s.answerPort == 0 {
		return true
	}
	return s.answerDirection != "recvonly" && s.answerDirection != "sendrecv"
}

// logPublishedVideoNegotiation logs every video section the client publishes
// in offerSDP next to the server's answer, so a screen or camera that never
// starts can be traced to a rejected section or an unexpected codec.
func (v *Voice) logPublishedVideoNegotiation(clientID, offerSDP, answerSDP string) {
	sections, err := publishedVideoSections(offerSDP, answerSDP)
	if err != nil {
		v.logger.Warn("webrtc published video negotiation unreadable",
			zap.String("client_id", clientID), zap.Error(err))
		return
	}
	for _, section := range sections {
		slot := v.router.slotFor(clientID, section.trackID, webrtc.RTPCodecTypeVideo)
		fields := []zap.Field{
			zap.String("client_id", clientID),
			zap.String("slot", slot),
			zap.String("mid", section.mid),
			zap.String("track_id", section.trackID),
			zap.String("offer_direction", section.offerDirection),
			zap.Strings("offer_formats", section.offerFormats),
			zap.Bool("answered", section.answered),
			zap.Int("answer_port", section.answerPort),
			zap.String("answer_direction", section.answerDirection),
			zap.Strings("answer_formats", section.answerFormats),
		}
		if section.rejected() {
			v.logger.Warn("webrtc published video rejected in answer", fields...)
			continue
		}
		v.logger.Info("webrtc published video negotiated", fields...)
	}
}

// publishedVideoSections pairs the client's sending video sections with the
// answer's sections by mid.
func publishedVideoSections(offerSDP, answerSDP string) ([]publishedVideoSection, error) {
	var offer, answer sdp.SessionDescription
	if err := offer.Unmarshal([]byte(offerSDP)); err != nil {
		return nil, err
	}
	if err := answer.Unmarshal([]byte(answerSDP)); err != nil {
		return nil, err
	}
	answers := make(map[string]*sdp.MediaDescription, len(answer.MediaDescriptions))
	for _, media := range answer.MediaDescriptions {
		if mid, ok := media.Attribute("mid"); ok {
			answers[mid] = media
		}
	}
	var sections []publishedVideoSection
	for _, media := range offer.MediaDescriptions {
		if media.MediaName.Media != "video" {
			continue
		}
		direction := mediaDirection(media)
		if direction != "sendonly" && direction != "sendrecv" {
			continue
		}
		mid, _ := media.Attribute("mid")
		section := publishedVideoSection{
			mid:            mid,
			trackID:        msidTrackID(media),
			offerDirection: direction,
			offerFormats:   videoFormats(media),
		}
		if reply, ok := answers[mid]; ok && mid != "" {
			section.answered = true
			section.answerPort = reply.MediaName.Port.Value
			section.answerDirection = mediaDirection(reply)
			section.answerFormats = videoFormats(reply)
		}
		sections = append(sections, section)
	}
	return sections, nil
}

func mediaDirection(media *sdp.MediaDescription) string {
	for _, direction := range []string{"sendrecv", "sendonly", "recvonly", "inactive"} {
		if _, ok := media.Attribute(direction); ok {
			return direction
		}
	}
	return "sendrecv"
}

func msidTrackID(media *sdp.MediaDescription) string {
	value, ok := media.Attribute("msid")
	if !ok {
		return ""
	}
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}

// videoFormats renders each payload as "pt name/clock [fmtp]" in offer order.
func videoFormats(media *sdp.MediaDescription) []string {
	names := make(map[string]string)
	params := make(map[string]string)
	for _, attribute := range media.Attributes {
		payload, rest, ok := strings.Cut(attribute.Value, " ")
		if !ok {
			continue
		}
		if _, err := strconv.ParseUint(payload, 10, 7); err != nil {
			continue
		}
		switch attribute.Key {
		case "rtpmap":
			names[payload] = rest
		case "fmtp":
			params[payload] = rest
		}
	}
	formats := make([]string, 0, min(len(media.MediaName.Formats), maxLoggedVideoFormats))
	for _, payload := range media.MediaName.Formats {
		if len(formats) == maxLoggedVideoFormats {
			formats = append(formats, "...")
			break
		}
		entry := payload + " " + names[payload]
		if fmtp := params[payload]; fmtp != "" {
			entry += " [" + fmtp + "]"
		}
		formats = append(formats, entry)
	}
	return formats
}
