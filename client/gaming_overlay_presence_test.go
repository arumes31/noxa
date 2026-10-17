package main

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func presenceMembers(speaking ...bool) []GamingOverlaySpeaker {
	names := []string{"Alex", "Sam", "Robin"}
	out := make([]GamingOverlaySpeaker, 0, len(speaking))
	for index, talking := range speaking {
		out = append(out, GamingOverlaySpeaker{ID: strings.ToLower(names[index]), Name: names[index], Speaking: talking})
	}
	return out
}

func presenceSnapshot(settings Settings, members []GamingOverlaySpeaker, peek ...GamingOverlayPeek) GamingOverlaySnapshot {
	return overlayPresentation(GamingOverlaySnapshot{Active: true, Speakers: members, Peek: peek}, settings)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestOverlayQuietMembersHoldThreeSecondsThenFade(t *testing.T) {
	settings := DefaultSettings()
	var p overlayPresence
	t0 := time.Unix(100, 0)
	if scene := p.scene(presenceSnapshot(settings, presenceMembers(true, true)), t0); len(scene.rowAlpha) != 2 {
		t.Fatalf("both speakers must show: %+v", scene.rowAlpha)
	}
	quiet := presenceSnapshot(settings, presenceMembers(true, false))
	for _, step := range []struct {
		at       time.Duration
		alpha    float64
		changing bool
	}{
		{0, 1, false}, {2999 * time.Millisecond, 1, false}, {3 * time.Second, 1, true},
		{3300 * time.Millisecond, .5, true}, {3540 * time.Millisecond, .1, true},
	} {
		scene := p.scene(quiet, t0.Add(step.at))
		if len(scene.rowAlpha) != 2 || !near(scene.rowAlpha[1], step.alpha) || scene.changing != step.changing {
			t.Fatalf("at %v: alpha %v changing %v, want %v/%v", step.at, scene.rowAlpha, scene.changing, step.alpha, step.changing)
		}
		if scene.snapshot.Speakers[1].Speaking || !scene.snapshot.Speakers[0].Speaking {
			t.Fatalf("speaking flags wrong at %v: %+v", step.at, scene.snapshot.Speakers)
		}
	}
	scene := p.scene(quiet, t0.Add(3600*time.Millisecond))
	if len(scene.snapshot.Speakers) != 1 || scene.snapshot.Speakers[0].Name != "Alex" || scene.changing {
		t.Fatalf("faded member must leave the layout: %+v", scene.snapshot.Speakers)
	}
	// Speaking again restarts the hold from the last speaking tick.
	p.scene(presenceSnapshot(settings, presenceMembers(true, true)), t0.Add(4*time.Second))
	if scene := p.scene(quiet, t0.Add(6900*time.Millisecond)); len(scene.rowAlpha) != 2 || scene.rowAlpha[1] != 1 {
		t.Fatalf("hold did not restart: %v", scene.rowAlpha)
	}
}

func TestOverlayReducedMotionHidesQuietMembersInstantly(t *testing.T) {
	for _, settings := range []Settings{
		func() Settings { s := DefaultSettings(); s.ReduceMotion = true; return s }(),
		func() Settings { s := DefaultSettings(); s.GamingOverlayAnimate = false; return s }(),
	} {
		var p overlayPresence
		t0 := time.Unix(100, 0)
		p.scene(presenceSnapshot(settings, presenceMembers(true, true)), t0)
		quiet := presenceSnapshot(settings, presenceMembers(true, false))
		if scene := p.scene(quiet, t0.Add(2900*time.Millisecond)); len(scene.rowAlpha) != 2 || scene.rowAlpha[1] != 1 {
			t.Fatalf("hold must still apply: %v", scene.rowAlpha)
		}
		if scene := p.scene(quiet, t0.Add(3*time.Second)); len(scene.rowAlpha) != 1 || scene.changing {
			t.Fatalf("reduced motion must hide at once: %v changing=%v", scene.rowAlpha, scene.changing)
		}
	}
}

func TestOverlayMembersWhoLeaveOrWereNeverHeardDoNotLinger(t *testing.T) {
	settings := DefaultSettings()
	var p overlayPresence
	t0 := time.Unix(100, 0)
	if scene := p.scene(presenceSnapshot(settings, presenceMembers(false, false)), t0); scene.visible() {
		t.Fatal("members who never spoke must not appear with only-speaking on")
	}
	p.scene(presenceSnapshot(settings, presenceMembers(true, true)), t0)
	if scene := p.scene(presenceSnapshot(settings, presenceMembers(true)), t0.Add(time.Second)); len(scene.rowAlpha) != 1 {
		t.Fatalf("a member missing from the snapshot left (or was muted) and must vanish: %v", scene.rowAlpha)
	}
	if scene := p.scene(presenceSnapshot(settings, presenceMembers(true, false)), t0.Add(2*time.Second)); len(scene.rowAlpha) != 1 {
		t.Fatal("a returning member must not resume an old fade")
	}
	// Muting the overlay (inactive snapshot) forgets every member at once.
	p.scene(GamingOverlaySnapshot{}, t0.Add(3*time.Second))
	if scene := p.scene(presenceSnapshot(settings, presenceMembers(false)), t0.Add(3100*time.Millisecond)); scene.visible() {
		t.Fatal("inactive overlay must reset fades")
	}
}

func TestOverlayAllMembersModeKeepsQuietMembersStill(t *testing.T) {
	settings := DefaultSettings()
	settings.GamingOverlayOnlySpeaking = false
	var p overlayPresence
	scene := p.scene(presenceSnapshot(settings, presenceMembers(true, false, false)), time.Unix(100, 0))
	if len(scene.rowAlpha) != 3 || scene.changing || scene.hideable {
		t.Fatalf("all members must stay visible: %+v", scene)
	}
	for index, alpha := range scene.rowAlpha {
		if alpha != 1 || scene.snapshot.Speakers[index].Speaking != (index == 0) {
			t.Fatalf("row %d: alpha %v speaking %v", index, alpha, scene.snapshot.Speakers[index].Speaking)
		}
	}
	if later := p.scene(presenceSnapshot(settings, presenceMembers(false, false, false)), time.Unix(200, 0)); len(later.rowAlpha) != 3 {
		t.Fatal("quiet members must not fade with only-speaking off")
	}
	// Muted members are listed, but never with a speaking indicator.
	muted := presenceMembers(true, false)
	muted[0].Muted = true
	if scene := p.scene(presenceSnapshot(settings, muted), time.Unix(300, 0)); scene.snapshot.Speakers[0].Speaking {
		t.Fatal("muted member shown as speaking")
	}
}

func TestOverlayChatPeekLifetimeAndBounds(t *testing.T) {
	settings := DefaultSettings()
	if lines := presenceSnapshot(settings, nil, GamingOverlayPeek{ID: "1", Name: "Sam", Text: "hi"}).Peek; len(lines) != 0 {
		t.Fatal("chat peek must stay off by default, whatever the frontend sends")
	}
	settings.GamingOverlayChatPeek = true
	long := GamingOverlayPeek{ID: "4", Name: strings.Repeat("N", 80), Text: strings.Repeat("x", 400) + "\x00‮"}
	lines := []GamingOverlayPeek{{ID: "1", Name: "A", Text: "one"}, {ID: "2", Name: "B", Text: "two"}, {ID: "3", Name: "C", Text: ""}, long}
	s := presenceSnapshot(settings, nil, lines...)
	if !s.Active || len(s.Peek) != 2 || s.Peek[0].ID != "2" || s.Peek[1].ID != "4" {
		t.Fatalf("peek must keep the newest three non-empty lines: %+v", s.Peek)
	}
	if len([]rune(s.Peek[1].Name)) != 32 || len([]rune(s.Peek[1].Text)) != 120 || strings.ContainsAny(s.Peek[1].Text, "\x00‮") {
		t.Fatalf("peek line not bounded: %+v", s.Peek[1])
	}
	var p overlayPresence
	t0 := time.Unix(100, 0)
	one := presenceSnapshot(settings, nil, GamingOverlayPeek{ID: "m1", Name: "Sam", Text: "gg"})
	for _, step := range []struct {
		at    time.Duration
		alpha float64
		lines int
	}{{0, 1, 1}, {5399 * time.Millisecond, 1, 1}, {5700 * time.Millisecond, .5, 1}, {6 * time.Second, 0, 0}, {6500 * time.Millisecond, 0, 0}} {
		scene := p.scene(one, t0.Add(step.at))
		if len(scene.peekAlpha) != step.lines || (step.lines > 0 && !near(scene.peekAlpha[0], step.alpha)) {
			t.Fatalf("at %v: %v, want %v lines at %v", step.at, scene.peekAlpha, step.lines, step.alpha)
		}
	}
	// A new message gets its own six seconds; the expired one never returns.
	two := presenceSnapshot(settings, nil, GamingOverlayPeek{ID: "m1", Name: "Sam", Text: "gg"}, GamingOverlayPeek{ID: "m2", Name: "Alex", Text: "again"})
	scene := p.scene(two, t0.Add(7*time.Second))
	if len(scene.snapshot.Peek) != 1 || scene.snapshot.Peek[0].ID != "m2" {
		t.Fatalf("peek queue = %+v", scene.snapshot.Peek)
	}
	settings.GamingOverlayAnimate = false
	var still overlayPresence
	still.scene(presenceSnapshot(settings, nil, GamingOverlayPeek{ID: "x", Name: "S", Text: "t"}), t0)
	if scene := still.scene(presenceSnapshot(settings, nil, GamingOverlayPeek{ID: "x", Name: "S", Text: "t"}), t0.Add(5400*time.Millisecond)); scene.visible() {
		t.Fatal("reduced motion must remove the peek line at once")
	}
}

func TestOverlayAutoHideStateMachine(t *testing.T) {
	settings := DefaultSettings()
	settings.GamingOverlayOnlySpeaking = false
	settings.GamingOverlayAutoHide = true
	settings.GamingOverlayChatPeek = true
	snapshot := presenceSnapshot(settings, presenceMembers(true, false), GamingOverlayPeek{ID: "m", Name: "Sam", Text: "hi"})
	var p overlayPresence
	t0 := time.Unix(100, 0)
	p.observeCursor(10, 10, t0) // reference sample only
	scene := p.scene(snapshot, t0)
	if !scene.hideable || scene.rowAlpha[1] != 1 || scene.peekAlpha[0] != 1 {
		t.Fatalf("content must start visible: %+v", scene)
	}
	if p.observeCursor(10, 10, t0.Add(time.Second)) {
		t.Fatal("a still cursor is not movement")
	}
	scene = p.scene(snapshot, t0.Add(2800*time.Millisecond))
	if scene.rowAlpha[0] != 1 || !near(scene.rowAlpha[1], .5) || !near(scene.peekAlpha[0], .5) || !scene.changing {
		t.Fatalf("idle fade wrong: rows %v peek %v", scene.rowAlpha, scene.peekAlpha)
	}
	scene = p.scene(snapshot, t0.Add(3200*time.Millisecond))
	if scene.rowAlpha[0] != 1 || scene.rowAlpha[1] != 0 || scene.peekAlpha[0] != 0 || !scene.visible() || scene.changing {
		t.Fatalf("idle content must be hidden, speakers kept: rows %v peek %v", scene.rowAlpha, scene.peekAlpha)
	}
	if len(scene.rowAlpha) != 2 {
		t.Fatal("hidden rows keep their place so speakers do not jump")
	}
	if !p.observeCursor(11, 10, t0.Add(4*time.Second)) {
		t.Fatal("movement not detected")
	}
	if scene = p.scene(snapshot, t0.Add(4*time.Second)); scene.rowAlpha[1] != 1 || scene.peekAlpha[0] != 1 {
		t.Fatalf("movement must show content again at once: %+v", scene)
	}
	// Nothing hideable (only speakers): no polling needed.
	speakersOnly := presenceSnapshot(settings, presenceMembers(true))
	if scene = p.scene(speakersOnly, t0.Add(9*time.Second)); scene.hideable || scene.rowAlpha[0] != 1 {
		t.Fatalf("speakers are never auto-hidden: %+v", scene)
	}
	// With only quiet content left and the mouse idle, the window has nothing to draw.
	quiet := presenceSnapshot(settings, presenceMembers(false, false))
	if scene = p.scene(quiet, t0.Add(9*time.Second)); scene.visible() || !scene.hideable {
		t.Fatalf("idle quiet-only overlay must be invisible but still polled: %+v", scene)
	}
	// Reduced motion: an instant hide at 2.5 s.
	settings.ReduceMotion = true
	var still overlayPresence
	quiet = presenceSnapshot(settings, presenceMembers(false))
	still.scene(quiet, t0)
	if scene = still.scene(quiet, t0.Add(2499*time.Millisecond)); !scene.visible() {
		t.Fatal("hidden too early")
	}
	if scene = still.scene(quiet, t0.Add(2500*time.Millisecond)); scene.visible() || scene.changing {
		t.Fatal("reduced motion must hide at once")
	}
	// Off: never hidden.
	settings.GamingOverlayAutoHide = false
	var off overlayPresence
	quiet = presenceSnapshot(settings, presenceMembers(false))
	off.scene(quiet, t0)
	if scene = off.scene(quiet, t0.Add(time.Hour)); !scene.visible() || scene.hideable {
		t.Fatal("auto-hide off must keep content")
	}
}

func TestOverlayRendererFadesBandsAndSkipsQuietAnimation(t *testing.T) {
	settings := DefaultSettings()
	settings.GamingOverlayScale = 100
	settings.GamingOverlayOnlySpeaking = false
	settings.GamingOverlayChatPeek = true
	var p overlayPresence
	scene := p.scene(presenceSnapshot(settings, presenceMembers(true, false), GamingOverlayPeek{ID: "m", Name: "Sam", Text: "hello there"}), time.Unix(1, 0))
	r, err := newOverlayRenderer(scene.snapshot, 2000, 2000, mistTestLabel)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.bands) != 3 || r.bands[0].area.Min.Y != 0 || r.bands[2].area.Max.Y != r.base.Bounds().Max.Y {
		t.Fatalf("bands do not partition the surface: %+v", r.bands)
	}
	for index := 1; index < len(r.bands); index++ {
		if r.bands[index].area.Min.Y != r.bands[index-1].area.Max.Y {
			t.Fatalf("gap between bands: %+v", r.bands)
		}
	}
	r.apply(scene)
	first := bytes.Clone(r.render(0).Pix)
	second := r.render(.3)
	quiet := r.bands[1].area
	// The speaking row's halo may reach the first pixels of the next band.
	for y := quiet.Min.Y + 4; y < quiet.Max.Y; y++ {
		for x := quiet.Min.X; x < quiet.Max.X; x++ {
			i := second.PixOffset(x, y)
			if !bytes.Equal(first[i:i+4], second.Pix[i:i+4]) {
				t.Fatalf("quiet row animated at %d,%d", x, y)
			}
		}
	}
	// Peek text sits below the speakers and is drawn without a panel: the
	// band's pixels outside glyphs stay transparent.
	peek := r.bands[2].area
	opaque, total := 0, 0
	for y := peek.Min.Y; y < peek.Max.Y; y++ {
		for x := peek.Min.X; x < peek.Max.X; x++ {
			total++
			if second.RGBAAt(x, y).A > 0 {
				opaque++
			}
		}
	}
	if opaque == 0 || opaque*4 > total {
		t.Fatalf("peek line should be bare text, %d of %d pixels drawn", opaque, total)
	}
	// Half opacity halves the quiet row's static pixels.
	full := second.RGBAAt(38, 164)
	scene.rowAlpha[1] = .5
	r.apply(scene)
	half := r.render(.3).RGBAAt(38, 164)
	if full.A == 0 || math.Abs(float64(half.A)-float64(full.A)/2) > 2 {
		t.Fatalf("faded ring alpha %d, full %d", half.A, full.A)
	}
	scene.rowAlpha[1], scene.peekAlpha[0] = 0, 0
	r.apply(scene)
	hidden := r.render(.3)
	for _, area := range []struct{ min, max int }{{quiet.Min.Y, quiet.Max.Y}, {peek.Min.Y, peek.Max.Y}} {
		for y := area.min; y < area.max; y++ {
			for x := 0; x < hidden.Bounds().Dx(); x++ {
				if hidden.RGBAAt(x, y).A != 0 && y >= quiet.Min.Y+4 {
					t.Fatalf("hidden content drawn at %d,%d", x, y)
				}
			}
		}
	}
}

func TestOverlayPeekReservesSpaceSoSpeakersStayPut(t *testing.T) {
	monitor := GamingOverlayMonitor{workWidth: 1920, workHeight: 1080}
	settings := DefaultSettings()
	settings.GamingOverlayChatPeek = true
	with := presenceSnapshot(settings, presenceMembers(true), GamingOverlayPeek{ID: "1", Name: "Sam", Text: "hi"})
	without := presenceSnapshot(settings, presenceMembers(true))
	_, y1, w1, h1 := gamingOverlayPlacement(with, monitor)
	_, y2, w2, h2 := gamingOverlayPlacement(without, monitor)
	if y1 != y2 || w1 != w2 || h1 != h2 {
		t.Fatalf("a peek line moved the overlay: %d/%dx%d vs %d/%dx%d", y1, w1, h1, y2, w2, h2)
	}
	settings.GamingOverlayChatPeek = false
	_, _, w3, h3 := gamingOverlayPlacement(presenceSnapshot(settings, presenceMembers(true)), monitor)
	if w3 != (88+4*24)*80/100 || h3 != 184*80/100 {
		t.Fatalf("overlay geometry changed with chat peek off: %dx%d", w3, h3)
	}
}

func TestOverlayBehaviourSettingsPersistAndDefault(t *testing.T) {
	def := DefaultSettings()
	if !def.GamingOverlayOnlySpeaking || def.GamingOverlayChatPeek || def.GamingOverlayAutoHide {
		t.Fatalf("defaults = %v/%v/%v", def.GamingOverlayOnlySpeaking, def.GamingOverlayChatPeek, def.GamingOverlayAutoHide)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	// A version-13 file written before these keys existed keeps the defaults,
	// and a legacy all-members key cannot expose quiet members.
	legacy := []byte(`{"settings_version":13,"gaming_overlay_speakers_only":false,"gaming_overlay_scale":120}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadSettingsAt(path)
	if !got.GamingOverlayOnlySpeaking || got.GamingOverlayChatPeek || got.GamingOverlayAutoHide || got.GamingOverlayScale != 120 || got.SettingsVersion != 13 {
		t.Fatalf("legacy load = %+v", got)
	}
	a := &App{settings: DefaultSettings(), hotkeys: map[string]*hotkeyReg{}, settingsPath: path}
	next := a.GetSettings()
	next.GamingOverlayOnlySpeaking, next.GamingOverlayChatPeek, next.GamingOverlayAutoHide = false, true, true
	if msg := a.SaveSettings(next); msg != "" {
		t.Fatal(msg)
	}
	saved := loadSettingsAt(path)
	if saved.GamingOverlayOnlySpeaking || !saved.GamingOverlayChatPeek || !saved.GamingOverlayAutoHide {
		t.Fatalf("round trip = %v/%v/%v", saved.GamingOverlayOnlySpeaking, saved.GamingOverlayChatPeek, saved.GamingOverlayAutoHide)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"gaming_overlay_only_speaking": false`, `"gaming_overlay_chat_peek": true`, `"gaming_overlay_auto_hide": true`} {
		if !bytes.Contains(data, []byte(key)) {
			t.Fatalf("saved file lacks %s", key)
		}
	}
	// The saved preferences drive the native snapshot; the caller cannot.
	s := overlayPresentation(GamingOverlaySnapshot{Active: true, OnlySpeaking: true, ChatPeek: false, AutoHide: false,
		Speakers: presenceMembers(true)}, saved)
	if s.OnlySpeaking || !s.ChatPeek || !s.AutoHide || !s.Fade {
		t.Fatalf("presentation ignored saved behaviours: %+v", s)
	}
}
