package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func validOverlayColor(value string) bool {
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	_, err := strconv.ParseUint(value[1:], 16, 24)
	return err == nil
}

func normalizeOverlayColors(s *Settings) {
	if !validOverlayColor(s.GamingOverlaySelfColor) {
		s.GamingOverlaySelfColor = "#43c798"
	}
	s.GamingOverlaySelfColor = strings.ToLower(s.GamingOverlaySelfColor)
	if s.GamingOverlayBlueStart == 0 {
		s.GamingOverlayBlueStart = 195
	}
	if s.GamingOverlayBlueEnd == 0 {
		s.GamingOverlayBlueEnd = 235
	}
	s.GamingOverlayBlueStart = clampSetting(s.GamingOverlayBlueStart, 180, 220)
	s.GamingOverlayBlueEnd = max(s.GamingOverlayBlueStart+20, clampSetting(s.GamingOverlayBlueEnd, 200, 240))
}

func overlayBlueColor(hue int) string {
	green := int(math.Round(224 - 170*float64(hue-180)/60))
	return fmt.Sprintf("#36%02xe0", green)
}
