package main

import (
	"strings"
	"unicode"

	"noxa/internal/safecast"
)

// GamingOverlaySnapshot contains display-only voice state, never session keys.
type GamingOverlaySnapshot struct {
	Active       bool                   `json:"active"`
	Animate      bool                   `json:"animate"`
	Style        string                 `json:"style"`
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
	ID       string `json:"id"`
	Avatar   string `json:"avatar"`
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
	s.Style = normalizeOverlayStyle(s.Style)
	switch s.Position {
	case "center-left", "top-left", "top-right", "bottom-left", "bottom-right", "custom":
	default:
		s.Position = "center-left"
	}
	if s.Scale == 0 {
		s.Scale = 80
	}
	if s.Opacity == 0 {
		s.Opacity = 88
	}
	s.Scale = clampSetting(s.Scale, 75, 200)
	s.Opacity = clampSetting(s.Opacity, 20, 100)
	s.X, s.Y = clampSetting(s.X, 0, 100), clampSetting(s.Y, 0, 100)
	s.Monitor = overlayText(s.Monitor, 128)
	visible := make([]GamingOverlaySpeaker, 0, min(8, len(s.Speakers)))
	for _, speaker := range s.Speakers {
		if !speaker.Speaking || speaker.Muted {
			continue
		}
		speaker.Name = overlayText(speaker.Name, 48)
		speaker.ID = overlayText(speaker.ID, 160)
		if len(speaker.Avatar) > 32768 {
			speaker.Avatar = ""
		}
		visible = append(visible, speaker)
		if len(visible) == 8 {
			break
		}
	}
	s.Speakers = visible
	if len(visible) == 0 {
		s.Active = false
		s.Title, s.Status, s.Notification = "", "", ""
	}
	return s
}

func (a *App) GamingOverlayAvailable() bool { return nativeGamingOverlayAvailable() }

func (a *App) GetGamingOverlayMonitors() []GamingOverlayMonitor { return nativeGamingOverlayMonitors() }

func overlayPresentation(s GamingOverlaySnapshot, settings Settings) GamingOverlaySnapshot {
	s.Animate = settings.GamingOverlayAnimate
	s.Style = settings.GamingOverlayStyle
	s.Position, s.Monitor = settings.GamingOverlayPosition, settings.GamingOverlayMonitor
	s.Scale, s.Opacity = settings.GamingOverlayScale, settings.GamingOverlayOpacity
	s.X, s.Y = settings.GamingOverlayX, settings.GamingOverlayY
	return normalizeGamingOverlay(s)
}

// PreviewGamingOverlay uses only bounded presentation preferences; names are
// examples, and no account, channel or notification data enters the preview.
func (a *App) PreviewGamingOverlay(settings Settings) string {
	s := overlayPresentation(GamingOverlaySnapshot{Active: true, Speakers: []GamingOverlaySpeaker{{ID: "preview-alex", Name: "Alex", Speaking: true}, {ID: "preview-sam", Name: "Sam", Speaking: true}}}, settings)
	a.overlayMu.Lock()
	defer a.overlayMu.Unlock()
	if a.overlayStopped || a.ctx == nil {
		return ""
	}
	if a.overlay == nil {
		if message := a.createGamingOverlayLocked(); message != "" {
			return message
		}
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
	s.Active = true // Placement also serves the empty settings preview.
	s = normalizeGamingOverlay(s)
	// Normalization bounds scale to 75–200, coordinates to 0–100 and
	// speakers to eight. Checked conversions also make the native ABI explicit.
	scale, _ := safecast.IntToInt32(s.Scale)
	rows, _ := safecast.IntToInt32(max(1, len(s.Speakers)))
	width = min(280*scale/100, monitor.workWidth)
	height = min((rows*76-12)*scale/100, monitor.workHeight)
	if s.Style == "mist-aurora" {
		longest := 1
		for _, person := range s.Speakers {
			longest = max(longest, len([]rune(person.Name)))
		}
		nameWidth, _ := safecast.IntToInt32(longest * 24)
		width = min((88+nameWidth)*scale/100, monitor.workWidth)
		height = min(rows*184*scale/100, monitor.workHeight)
	}
	return gamingOverlayPlacementSized(s, monitor, width, height)
}

func normalizeOverlayStyle(style string) string {
	if style == "mist-aurora" {
		return style
	}
	return "bars"
}

func gamingOverlayPlacementSized(s GamingOverlaySnapshot, monitor GamingOverlayMonitor, width, height int32) (x, y, w, h int32) {
	margin := min(int32(16), max(int32(0), min(monitor.workWidth-width, monitor.workHeight-height)/2))
	x, y = monitor.workWidth-width-margin, margin
	if strings.HasSuffix(s.Position, "left") {
		x = margin
	}
	if strings.HasPrefix(s.Position, "bottom") {
		y = monitor.workHeight - height - margin
	}
	if s.Position == "center-left" {
		// Slightly above center leaves the desktop taskbar/HUD clear. Keep the
		// anchor fixed as active speakers expand the stack in both directions.
		y = monitor.workHeight*45/100 - height/2
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
		if message := a.createGamingOverlayLocked(); message != "" {
			return message
		}
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
