package main

import (
	"strings"
	"unicode"

	"noxa/internal/safecast"
)

// GamingOverlaySnapshot contains display-only voice state, never session keys.
type GamingOverlaySnapshot struct {
	Active       bool                   `json:"active"`
	Title        string                 `json:"title"`
	Status       string                 `json:"status"`
	Speakers     []GamingOverlaySpeaker `json:"speakers"`
	Notification string                 `json:"notification"`
	Position     string                 `json:"position"`
	Monitor      string                 `json:"monitor"`
	Scale        int                    `json:"scale"`
	Opacity      int                    `json:"opacity"`
	X            int                    `json:"x"`
	Y            int                    `json:"y"`
}

type GamingOverlayMonitor struct {
	ID                                       string `json:"id"`
	Name                                     string `json:"name"`
	Width                                    int32  `json:"width"`
	Height                                   int32  `json:"height"`
	Primary                                  bool   `json:"primary"`
	workLeft, workTop, workWidth, workHeight int32
}

type GamingOverlaySpeaker struct {
	Name     string `json:"name"`
	Speaking bool   `json:"speaking"`
	Muted    bool   `json:"muted"`
}

type gamingOverlayWindow interface {
	Update(GamingOverlaySnapshot)
	Preview(GamingOverlaySnapshot)
	Close()
}

func overlayText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
	chars := []rune(strings.TrimSpace(value))
	if len(chars) > limit {
		chars = chars[:limit]
	}
	return string(chars)
}

func normalizeGamingOverlay(s GamingOverlaySnapshot) GamingOverlaySnapshot {
	if !s.Active {
		return GamingOverlaySnapshot{}
	}
	s.Title = overlayText(s.Title, 64)
	s.Status = overlayText(s.Status, 64)
	s.Notification = overlayText(s.Notification, 140)
	switch s.Position {
	case "top-left", "top-right", "bottom-left", "bottom-right", "custom":
	default:
		s.Position = "top-right"
	}
	if s.Scale == 0 {
		s.Scale = 100
	}
	if s.Opacity == 0 {
		s.Opacity = 88
	}
	s.Scale = clampSetting(s.Scale, 75, 200)
	s.Opacity = clampSetting(s.Opacity, 20, 100)
	s.X, s.Y = clampSetting(s.X, 0, 100), clampSetting(s.Y, 0, 100)
	s.Monitor = overlayText(s.Monitor, 128)
	if len(s.Speakers) > 8 {
		s.Speakers = s.Speakers[:8]
	}
	s.Speakers = append([]GamingOverlaySpeaker(nil), s.Speakers...)
	for i := range s.Speakers {
		s.Speakers[i].Name = overlayText(s.Speakers[i].Name, 48)
	}
	return s
}

func gamingOverlayText(s GamingOverlaySnapshot) string {
	lines := []string{"noXa · " + s.Title, s.Status}
	for _, speaker := range s.Speakers {
		prefix := "· "
		if speaker.Speaking {
			prefix = "● "
		} else if speaker.Muted {
			prefix = "× "
		}
		lines = append(lines, prefix+speaker.Name)
	}
	if s.Notification != "" {
		lines = append(lines, s.Notification)
	}
	return strings.Join(lines, "\n")
}

func (a *App) GamingOverlayAvailable() bool { return nativeGamingOverlayAvailable() }

func (a *App) GetGamingOverlayMonitors() []GamingOverlayMonitor { return nativeGamingOverlayMonitors() }

func overlayPresentation(s GamingOverlaySnapshot, settings Settings) GamingOverlaySnapshot {
	s.Position, s.Monitor = settings.GamingOverlayPosition, settings.GamingOverlayMonitor
	s.Scale, s.Opacity = settings.GamingOverlayScale, settings.GamingOverlayOpacity
	s.X, s.Y = settings.GamingOverlayX, settings.GamingOverlayY
	if settings.GamingOverlaySpeakersOnly {
		visible := make([]GamingOverlaySpeaker, 0, len(s.Speakers))
		for _, speaker := range s.Speakers {
			if speaker.Speaking {
				visible = append(visible, speaker)
			}
		}
		s.Speakers = visible
	}
	return normalizeGamingOverlay(s)
}

// PreviewGamingOverlay uses only bounded presentation preferences; names are
// examples, and no account, channel or notification data enters the preview.
func (a *App) PreviewGamingOverlay(settings Settings) string {
	s := overlayPresentation(GamingOverlaySnapshot{Active: true, Title: "Overlay preview", Status: "Microphone muted", Speakers: []GamingOverlaySpeaker{{Name: "Alex", Speaking: true}, {Name: "Sam"}}}, settings)
	a.overlayMu.Lock()
	defer a.overlayMu.Unlock()
	if a.overlayStopped || a.ctx == nil {
		return ""
	}
	if a.overlay == nil {
		window, err := newNativeGamingOverlay()
		if err != nil {
			return err.Error()
		}
		a.overlay = window
	}
	a.overlay.Preview(s)
	return ""
}

func selectOverlayMonitor(monitors []GamingOverlayMonitor, id string) GamingOverlayMonitor {
	var primary GamingOverlayMonitor
	for _, monitor := range monitors {
		if monitor.ID == id {
			return monitor
		}
		if monitor.Primary || primary.ID == "" {
			primary = monitor
		}
	}
	return primary
}

func gamingOverlayPlacement(s GamingOverlaySnapshot, monitor GamingOverlayMonitor) (x, y, width, height int32) {
	s = normalizeGamingOverlay(s)
	// Normalization bounds scale to 75–200, coordinates to 0–100 and text
	// to twelve lines. Checked conversions also make the native ABI explicit.
	scale, _ := safecast.IntToInt32(s.Scale)
	lines, _ := safecast.IntToInt32(len(strings.Split(gamingOverlayText(s), "\n")))
	width = min(348*scale/100, monitor.workWidth)
	height = min((22+lines*23)*scale/100, monitor.workHeight)
	margin := min(int32(16), max(int32(0), min(monitor.workWidth-width, monitor.workHeight-height)/2))
	x, y = monitor.workWidth-width-margin, margin
	if strings.HasSuffix(s.Position, "left") {
		x = margin
	}
	if strings.HasPrefix(s.Position, "bottom") {
		y = monitor.workHeight - height - margin
	}
	if s.Position == "custom" {
		percentX, _ := safecast.IntToInt32(s.X)
		percentY, _ := safecast.IntToInt32(s.Y)
		x, y = (monitor.workWidth-width)*percentX/100, (monitor.workHeight-height)*percentY/100
	}
	return monitor.workLeft + max(int32(0), x), monitor.workTop + max(int32(0), y), width, height
}

// UpdateGamingOverlay is bounded at the native boundary as well as in the UI.
func (a *App) UpdateGamingOverlay(s GamingOverlaySnapshot) string {
	a.settingsMu.Lock()
	settings := a.settings
	a.settingsMu.Unlock()
	s.Active = s.Active && settings.GamingOverlay
	s = overlayPresentation(s, settings)
	a.overlayMu.Lock()
	defer a.overlayMu.Unlock()
	if a.overlayStopped || a.ctx == nil {
		return ""
	}
	if a.overlay == nil && s.Active {
		window, err := newNativeGamingOverlay()
		if err != nil {
			return err.Error()
		}
		a.overlay = window
	}
	if a.overlay != nil {
		a.overlay.Update(s)
	}
	return ""
}

func (a *App) closeGamingOverlay() {
	a.overlayMu.Lock()
	a.overlayStopped = true
	window := a.overlay
	a.overlay = nil
	a.overlayMu.Unlock()
	if window != nil {
		window.Close()
	}
}
