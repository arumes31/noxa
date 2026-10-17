package webrtc

import (
	"fmt"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/interceptor/pkg/report"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

// SetVideoBoundsForNewPeers replaces codec configuration for subsequently
// created peers without mutating existing Pion MediaEngine copies. The shared
// ICE socket, network settings and run certificate remain unchanged. Callers
// must separately update router limits and arrange existing peer rebuilds.
func (e *Engine) SetVideoBoundsForNewPeers(bounds VideoBounds) error {
	if err := bounds.validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	api, err := e.prepareVideoBoundsLocked(bounds)
	if err != nil {
		return err
	}
	e.api, e.videoBounds = api, bounds
	return nil
}

// The caller owns e.mu through installation of the returned API.
func (e *Engine) prepareVideoBoundsLocked(bounds VideoBounds) (*webrtc.API, error) {
	if e.closed {
		return nil, fmt.Errorf("webrtc: engine is closed")
	}
	if e.videoBounds == bounds {
		return e.api, nil
	}
	media, interceptors, err := newEngineMediaObserved(e.enableAV1, bounds, e.egress, e.upstreamFeedback)
	if err != nil {
		return nil, err
	}
	return webrtc.NewAPI(
		webrtc.WithMediaEngine(media),
		webrtc.WithSettingEngine(e.settingEngine),
		webrtc.WithInterceptorRegistry(interceptors),
	), nil
}

// Each API gets its own codec/interceptor registration. SettingEngine values
// are immutable after construction, as required by Pion's WithSettingEngine.
func newEngineMedia(enableAV1 bool, bounds VideoBounds, egress *mediaEgressRegistry) (*webrtc.MediaEngine, *interceptor.Registry, error) {
	return newEngineMediaObserved(enableAV1, bounds, egress, nil)
}

// innermost factories (tests only) sit next to the transport, inside every
// production interceptor.
func newEngineMediaObserved(enableAV1 bool, bounds VideoBounds, egress *mediaEgressRegistry, feedback *upstreamFeedbackRegistry, innermost ...interceptor.Factory) (*webrtc.MediaEngine, *interceptor.Registry, error) {
	media := &webrtc.MediaEngine{}
	if err := registerCodecsWithVideoBounds(media, enableAV1, bounds); err != nil {
		return nil, nil, fmt.Errorf("webrtc: registering codecs: %w", err)
	}
	registry := &interceptor.Registry{}
	for _, factory := range innermost {
		registry.Add(factory)
	}
	// Registered before defaults so their synchronous TWCC writes cross this observer.
	registry.Add(upstreamFeedbackFactory{registry: feedback})
	// Pion's separate RTX reader bypasses its primary NACK receive log.
	// Filter only requests for packets actually returned to our video reader.
	registry.Add(repairNACKFactory{})
	// The pacer checks final authority before assigning TWCC or accounting a
	// send. NACK/RTX also enters this queue and crosses the same boundary.
	registry.Add(mediaCCFactory{registry: egress})
	// The default TWCC recorder synchronously writes feedback. Dispatch its
	// immutable reports separately so a slow network write cannot hold RTP
	// reads and manufacture a gap in subsequent arrival timestamps.
	registry.Add(upstreamTWCCDispatchFactory{registry: feedback})
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeAudio, webrtc.RTPCodecTypeVideo} {
		if err := media.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: sdp.TransportCCURI}, kind); err != nil {
			return nil, nil, fmt.Errorf("webrtc: configuring TWCC egress: %w", err)
		}
	}
	// Retain missing video sequences for the same bounded packet window as
	// reordering. Pion's default 512 slots can forget a burst's loss before its
	// first 100 ms NACK tick. Twenty-six attempts cover the 2.5 s repair window
	// while still ending retries on an idle source. Default registration only
	// advertises NACK for video; the audio feedback policy remains unchanged.
	if err := registerEngineInterceptors(media, registry, egress); err != nil {
		return nil, nil, fmt.Errorf("webrtc: registering default interceptors: %w", err)
	}
	registry.Add(upstreamReadTimingOuterFactory{registry: feedback})
	return media, registry, nil
}

// registerEngineInterceptors is pion's RegisterDefaultInterceptorsWithOptions
// in the same order, except that egress-managed outbound streams get Sender
// Reports carrying the publisher's capture-time mapping instead of pion's
// server-clock reports (media_timing.go).
func registerEngineInterceptors(media *webrtc.MediaEngine, registry *interceptor.Registry, egress *mediaEgressRegistry) error {
	if err := webrtc.ConfigureNackWithOptions(media, registry,
		[]nack.GeneratorOption{nack.GeneratorSize(videoReorderPackets), nack.GeneratorMaxNacksPerPacket(26)},
	); err != nil {
		return err
	}
	receiverReports, err := report.NewReceiverInterceptor()
	if err != nil {
		return err
	}
	registry.Add(receiverReports)
	if egress != nil {
		registry.Add(sourceTimedReportFactory{registry: egress})
	} else {
		senderReports, err := report.NewSenderInterceptor()
		if err != nil {
			return err
		}
		registry.Add(senderReports)
	}
	if err := webrtc.ConfigureSimulcastExtensionHeaders(media); err != nil {
		return err
	}
	if err := webrtc.ConfigureStatsInterceptor(registry); err != nil {
		return err
	}
	return webrtc.ConfigureTWCCSender(media, registry)
}
