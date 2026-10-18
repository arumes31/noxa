package main

import "strconv"

// trayStatus is everything the tray icon, tooltip and taskbar reflect.
type trayStatus struct {
	connected bool // the active tab is connected
	inChannel bool // the frontend holds a live voice session in a channel
	speaking  bool
	muted     bool
	deafened  bool
	unread    int // unseen messages reported by the frontend
	mentions  int // unread mentions (backend + frontend reports)
}

type trayPresentation struct {
	icon           trayIconKey
	title, tooltip string
}

var trayStateLabels = [trayStateCount]string{
	trayIdle:            "In a channel",
	trayTalking:         "Talking",
	trayMicMuted:        "Microphone muted",
	trayDeafened:        "Audio muted",
	trayBothMuted:       "Microphone and audio muted",
	trayTalkingDeafened: "Talking · Audio muted",
	trayDisconnected:    "Not connected",
	trayNoChannel:       "Connected, not in a channel",
}

// trayStateFor applies the documented priority:
// not connected > mute states > not in a channel > in-channel idle/talking.
func trayStateFor(s trayStatus) trayIconState {
	speaking := s.connected && s.inChannel && s.speaking && !s.muted
	switch {
	case !s.connected:
		return trayDisconnected
	case s.muted && s.deafened:
		return trayBothMuted
	case s.muted:
		return trayMicMuted
	case speaking && s.deafened:
		return trayTalkingDeafened
	case s.deafened:
		return trayDeafened
	case !s.inChannel:
		return trayNoChannel
	case speaking:
		return trayTalking
	default:
		return trayIdle
	}
}

// Titles and tooltips are built once, so a voice transition without unread
// counts allocates nothing.
var trayTitles, trayTooltips = func() (titles, tooltips [trayStateCount]string) {
	for state, label := range trayStateLabels {
		titles[state], tooltips[state] = "noXa · "+label, "noXa — "+label
	}
	return
}()

func trayPresent(s trayStatus) trayPresentation {
	state := trayStateFor(s)
	p := trayPresentation{
		icon:    trayIconKey{state: state, unread: trayUnreadBucket(s.unread)},
		title:   trayTitles[state],
		tooltip: trayTooltips[state],
	}
	if s.unread > 0 {
		p.tooltip += " — " + strconv.Itoa(s.unread) + " unread message(s)"
	}
	if s.mentions > 0 {
		p.tooltip += " — " + strconv.Itoa(s.mentions) + " unread mention(s)"
	}
	return p
}
