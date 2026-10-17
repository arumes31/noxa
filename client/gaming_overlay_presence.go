package main

import "time"

// Presence timing for the in-game overlay behaviours. The native window owns
// one overlayPresence and asks it, per tick, which rows and chat lines are
// visible and at what opacity. Fades are plain opacity ramps; with Fade off
// (reduced motion) every hold ends in an instant hide.
//
// "Auto-hide HUD": the native overlay has no telemetry, dock or controls of
// its own, so auto-hide applies to its non-speaking content instead: quiet
// members (only-speaking off, or a member still fading out) and chat peek
// lines. Active speakers are never hidden by it.
const (
	overlayQuietHold    = 3 * time.Second
	overlayPeekHold     = 5400 * time.Millisecond // plus the fade: about 6 s on screen
	overlayIdleAfter    = 2500 * time.Millisecond
	overlayFadeDuration = 600 * time.Millisecond
	overlayCursorPoll   = 250 * time.Millisecond
)

type overlayPresence struct {
	spoke    map[string]time.Time // member key -> last tick seen speaking
	peekSeen map[string]time.Time // peek id -> first tick shown
	lastMove time.Time
	cursorX  int32
	cursorY  int32
	cursorOK bool
}

// overlayScene is what to draw now. snapshot.Speakers and snapshot.Peek hold
// only the visible entries; the alphas run parallel to them.
type overlayScene struct {
	snapshot  GamingOverlaySnapshot
	rowAlpha  []float64
	peekAlpha []float64
	changing  bool // a fade is in progress, so frames must keep presenting
	hideable  bool // auto-hide controls some visible content (poll the cursor)
}

// visible reports whether anything on the scene is drawn at all.
func (s overlayScene) visible() bool {
	for _, alpha := range s.rowAlpha {
		if alpha > 0 {
			return true
		}
	}
	for _, alpha := range s.peekAlpha {
		if alpha > 0 {
			return true
		}
	}
	return false
}

func overlayMemberKey(speaker GamingOverlaySpeaker) string {
	if speaker.ID != "" {
		return speaker.ID
	}
	return "name:" + speaker.Name
}

// overlayFadeOut returns the opacity after holding for hold, then fading over
// overlayFadeDuration (or vanishing at once without fades).
func overlayFadeOut(elapsed, hold time.Duration, fade bool) (alpha float64, changing bool) {
	if elapsed < hold {
		return 1, false
	}
	if !fade {
		return 0, false
	}
	progress := float64(elapsed-hold) / float64(overlayFadeDuration)
	if progress >= 1 {
		return 0, false
	}
	return 1 - progress, true
}

// observeCursor records a cursor sample. Any change counts as movement; the
// first sample after a reset only establishes the reference position.
func (p *overlayPresence) observeCursor(x, y int32, now time.Time) bool {
	moved := p.cursorOK && (x != p.cursorX || y != p.cursorY)
	p.cursorX, p.cursorY, p.cursorOK = x, y, true
	if moved {
		p.lastMove = now
	}
	return moved
}

// reset forgets members and chat lines (overlay hidden, muted, left voice).
// The cursor reference survives, so idle time is still measured correctly.
func (p *overlayPresence) reset() {
	p.spoke, p.peekSeen = nil, nil
}

func (p *overlayPresence) scene(s GamingOverlaySnapshot, now time.Time) overlayScene {
	if p.spoke == nil {
		p.spoke = map[string]time.Time{}
	}
	if p.peekSeen == nil {
		p.peekSeen = map[string]time.Time{}
	}
	scene := overlayScene{snapshot: s}
	scene.snapshot.Speakers, scene.snapshot.Peek = nil, nil
	if !s.Active {
		p.reset()
		return scene
	}
	idle, idleChanging := 1.0, false
	if s.AutoHide {
		if p.lastMove.IsZero() {
			p.lastMove = now
		}
		idle, idleChanging = overlayFadeOut(now.Sub(p.lastMove), overlayIdleAfter, s.Fade)
	}
	hideable := func(alpha float64, changing bool) float64 {
		scene.hideable = scene.hideable || s.AutoHide
		scene.changing = scene.changing || changing || (s.AutoHide && idleChanging)
		return alpha * idle
	}

	present := make(map[string]bool, len(s.Speakers))
	for _, speaker := range s.Speakers {
		key := overlayMemberKey(speaker)
		present[key] = true
		alpha := 1.0
		switch {
		case speaker.Speaking:
			p.spoke[key] = now
		case !s.OnlySpeaking:
			alpha = hideable(1, false)
		default:
			last, ok := p.spoke[key]
			if !ok {
				continue
			}
			fade, changing := overlayFadeOut(now.Sub(last), overlayQuietHold, s.Fade)
			if fade <= 0 {
				delete(p.spoke, key)
				continue
			}
			alpha = hideable(fade, changing)
		}
		scene.snapshot.Speakers = append(scene.snapshot.Speakers, speaker)
		scene.rowAlpha = append(scene.rowAlpha, alpha)
	}
	for key := range p.spoke {
		if !present[key] {
			delete(p.spoke, key) // Left the channel, or muted: gone at once.
		}
	}

	shown := make(map[string]bool, len(s.Peek))
	for _, line := range s.Peek {
		shown[line.ID] = true
		seen, ok := p.peekSeen[line.ID]
		if !ok {
			seen = now
			p.peekSeen[line.ID] = seen
		}
		// The record outlives the fade while the frontend still lists the
		// line, so an expired message never reappears.
		fade, changing := overlayFadeOut(now.Sub(seen), overlayPeekHold, s.Fade)
		if fade <= 0 {
			continue
		}
		scene.snapshot.Peek = append(scene.snapshot.Peek, line)
		scene.peekAlpha = append(scene.peekAlpha, hideable(fade, changing))
	}
	for id := range p.peekSeen {
		if !shown[id] {
			delete(p.peekSeen, id)
		}
	}
	return scene
}
