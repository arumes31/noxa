//go:build integration

package webrtc

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

type browserFanoutProfile struct {
	Capacity     bool   `json:"capacity"`
	Viewers      int    `json:"viewers"`
	StageSeconds int    `json:"stageSeconds"`
	SlowWriteMS  int    `json:"slowWriteMS"`
	Codec        string `json:"codec"`
	Workload     string `json:"workload"`
}

func parseBrowserFanoutProfile(viewers, seconds, delay string, options ...string) (browserFanoutProfile, error) {
	p := browserFanoutProfile{Viewers: 3, StageSeconds: 15, Workload: "pan"}
	if len(options) > 0 {
		p.Codec = options[0]
	}
	if len(options) > 1 && options[1] != "" {
		p.Workload = options[1]
	}
	if p.Workload != "pan" && (viewers == "" || p.Workload != "desktop") {
		return p, errors.New("NOXA_BROWSER_MEDIA_WORKLOAD must be pan or desktop; desktop requires capacity mode")
	}
	if p.Workload == "desktop" {
		p.StageSeconds = 60
		if seconds != "" && seconds != "60" {
			return p, errors.New("desktop workload requires 60-second stages to measure all three phases")
		}
	}
	if p.Codec != "" && (viewers == "" || p.Codec != "h264" && p.Codec != "vp8") {
		return p, errors.New("NOXA_BROWSER_MEDIA_CODEC requires capacity mode and h264 or vp8")
	}
	if viewers == "" {
		if seconds != "" || delay != "" {
			return p, errors.New("capacity stage/delay requires NOXA_BROWSER_MEDIA_VIEWERS")
		}
		return p, nil
	}
	p.Capacity = true
	var err error
	p.Viewers, err = strconv.Atoi(viewers)
	if err != nil || (p.Viewers != 1 && p.Viewers != 5 && p.Viewers != 20) {
		return p, errors.New("NOXA_BROWSER_MEDIA_VIEWERS must be 1, 5 or 20")
	}
	if seconds != "" {
		p.StageSeconds, err = strconv.Atoi(seconds)
		if err != nil || p.StageSeconds < 10 || p.StageSeconds > 60 {
			return p, errors.New("NOXA_BROWSER_MEDIA_STAGE_SECONDS must be 10..60")
		}
	}
	if delay != "" {
		p.SlowWriteMS, err = strconv.Atoi(delay)
		if err != nil || p.SlowWriteMS < 1 || p.SlowWriteMS > 20 || p.Viewers < 5 {
			return p, errors.New("NOXA_BROWSER_MEDIA_SLOW_WRITE_MS must be 1..20 with at least 5 viewers")
		}
	}
	return p, nil
}

type browserVideoWriteDelay struct {
	interceptor.NoOp
	ctx     context.Context
	delay   time.Duration
	until   atomic.Int64
	packets atomic.Uint64
}

func (d *browserVideoWriteDelay) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	if !strings.HasPrefix(strings.ToLower(info.MimeType), "video/") {
		return writer
	}
	return interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
		if time.Now().UnixNano() < d.until.Load() {
			d.packets.Add(1)
			timer := time.NewTimer(d.delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-d.ctx.Done():
				return 0, io.ErrClosedPipe
			}
		}
		return writer.Write(header, payload, attrs)
	})
}

type browserDelayedProductionFactory struct {
	base  *interceptor.Registry
	delay *browserVideoWriteDelay
	trace *browserMediaTrace
}

func (f browserDelayedProductionFactory) NewInterceptor(id string) (interceptor.Interceptor, error) {
	base, err := f.base.Build(id)
	if err != nil {
		return nil, err
	}
	// Pion wraps writers in registration order. The delay goes inside the
	// production pacer and its terminal authorization lease, not admission.
	chain := make([]interceptor.Interceptor, 0, 4)
	if f.trace != nil {
		chain = append(chain, &browserTraceInterceptor{trace: f.trace, pcID: id, final: true})
	}
	if f.delay != nil {
		chain = append(chain, f.delay)
	}
	chain = append(chain, base)
	if f.trace != nil {
		chain = append(chain, &browserTraceInterceptor{trace: f.trace, pcID: id})
	}
	return interceptor.NewChain(chain), nil
}

func installBrowserVideoWriteDelay(engine *Engine, delay *browserVideoWriteDelay, traces ...*browserMediaTrace) error {
	media, base, err := newEngineMedia(engine.enableAV1, engine.videoBounds, engine.egress)
	if err != nil {
		return err
	}
	registry := &interceptor.Registry{}
	var trace *browserMediaTrace
	if len(traces) > 0 {
		trace = traces[0]
	}
	registry.Add(browserDelayedProductionFactory{base: base, delay: delay, trace: trace})
	// Test-only API replacement occurs before this engine creates any peers.
	engine.api = pion.NewAPI(pion.WithMediaEngine(media), pion.WithSettingEngine(engine.settingEngine), pion.WithInterceptorRegistry(registry))
	return nil
}

func TestBrowserFanoutProfileBounds(t *testing.T) {
	for _, values := range [][3]string{{"", "", ""}, {"1", "10", ""}, {"5", "60", "20"}, {"20", "15", "1"}} {
		if _, err := parseBrowserFanoutProfile(values[0], values[1], values[2]); err != nil {
			t.Fatalf("valid profile %v: %v", values, err)
		}
	}
	for _, values := range [][3]string{{"2", "", ""}, {"21", "", ""}, {"5", "9", ""}, {"5", "61", ""}, {"5", "10", "21"}, {"5", "10", "0"}, {"1", "10", "1"}, {"", "10", ""}, {"", "", "1"}} {
		if _, err := parseBrowserFanoutProfile(values[0], values[1], values[2]); err == nil {
			t.Fatalf("accepted unbounded/ambiguous profile %v", values)
		}
	}
	for _, codec := range []string{"h264", "vp8"} {
		profile, err := parseBrowserFanoutProfile("1", "10", "", codec)
		if err != nil || profile.Codec != codec {
			t.Fatalf("valid codec %s: %v", codec, err)
		}
	}
	for _, values := range [][2]string{{"", "h264"}, {"1", "av1"}, {"1", "H264"}} {
		if _, err := parseBrowserFanoutProfile(values[0], "", "", values[1]); err == nil {
			t.Fatalf("accepted invalid codec profile %v", values)
		}
	}
	for _, viewers := range []string{"1", "5", "20"} {
		profile, err := parseBrowserFanoutProfile(viewers, "", "", "h264", "desktop")
		if err != nil || profile.Workload != "desktop" || profile.StageSeconds != 60 {
			t.Fatalf("desktop profile lost complete phase coverage: %+v, %v", profile, err)
		}
	}
	for _, values := range [][3]string{{"", "", "desktop"}, {"1", "10", "desktop"}, {"1", "60", "unknown"}} {
		if _, err := parseBrowserFanoutProfile(values[0], values[1], "", "", values[2]); err == nil {
			t.Fatalf("accepted incomplete/unknown workload %v", values)
		}
	}
	legacy, err := parseBrowserFanoutProfile("", "", "")
	if err != nil || legacy.Capacity || legacy.Workload != "pan" || legacy.StageSeconds != 15 {
		t.Fatalf("legacy high-entropy pan changed: %+v, %v", legacy, err)
	}
}

func TestBrowserVideoDelayExcludesAudioAndHonorsExpiryAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	delay := &browserVideoWriteDelay{ctx: ctx, delay: 20 * time.Millisecond}
	writes := 0
	writer := interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { writes++; return 1, nil })
	video := delay.BindLocalStream(&interceptor.StreamInfo{MimeType: "video/H264"}, writer)
	audio := delay.BindLocalStream(&interceptor.StreamInfo{MimeType: "audio/opus"}, writer)
	delay.until.Store(time.Now().Add(-time.Second).UnixNano())
	if _, err := video.Write(&rtp.Header{}, nil, nil); err != nil || writes != 1 || delay.packets.Load() != 0 {
		t.Fatal("expired delay altered video output")
	}
	delay.until.Store(time.Now().Add(time.Second).UnixNano())
	cancel()
	if _, err := audio.Write(&rtp.Header{}, nil, nil); err != nil || writes != 2 || delay.packets.Load() != 0 {
		t.Fatal("video impairment affected audio")
	}
	if _, err := video.Write(&rtp.Header{}, nil, nil); !errors.Is(err, io.ErrClosedPipe) || writes != 2 || delay.packets.Load() != 1 {
		t.Fatal("cancelled delay wrote a video packet")
	}
}
