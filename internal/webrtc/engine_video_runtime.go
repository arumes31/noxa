package webrtc

import (
	"fmt"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
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
	media, interceptors, err := newEngineMedia(e.enableAV1, bounds, e.egress)
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
	media := &webrtc.MediaEngine{}
	if err := registerCodecsWithVideoBounds(media, enableAV1, bounds); err != nil {
		return nil, nil, fmt.Errorf("webrtc: registering codecs: %w", err)
	}
	registry := &interceptor.Registry{}
	// The pacer checks final authority before assigning TWCC or accounting a
	// send. NACK/RTX also enters this queue and crosses the same boundary.
	registry.Add(mediaCCFactory{registry: egress})
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeAudio, webrtc.RTPCodecTypeVideo} {
		if err := media.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: sdp.TransportCCURI}, kind); err != nil {
			return nil, nil, fmt.Errorf("webrtc: configuring TWCC egress: %w", err)
		}
	}
	// At the default 100 ms interval, ten attempts give normal RTTs several
	// chances to repair loss. Unlimited retries keep obsolete packets alive
	// for tens of seconds on sparse video and indefinitely on an idle layer.
	if err := webrtc.RegisterDefaultInterceptorsWithOptions(media, registry,
		webrtc.WithNackGeneratorOptions(nack.GeneratorMaxNacksPerPacket(10)),
	); err != nil {
		return nil, nil, fmt.Errorf("webrtc: registering default interceptors: %w", err)
	}
	return media, registry, nil
}
