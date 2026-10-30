package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

const updateWindowEnv = "NOXA_UPDATE_WINDOW"

// Window placement is an ephemeral update handoff, not a shared profile file:
// parallel clients cannot overwrite each other's monitor or normal rectangle.
type updateWindowPlacement struct {
	Version   int   `json:"version"`
	Expires   int64 `json:"expires"`
	Left      int32 `json:"left"`
	Top       int32 `json:"top"`
	Right     int32 `json:"right"`
	Bottom    int32 `json:"bottom"`
	Maximized bool  `json:"maximized"`
}

func decodeUpdateWindow(raw string, now time.Time) (*updateWindowPlacement, error) {
	var p updateWindowPlacement
	if len(raw) > 1024 || json.Unmarshal([]byte(raw), &p) != nil || p.Version != 1 ||
		p.Expires < now.Unix() || p.Expires > now.Add(10*time.Minute).Unix() {
		return nil, fmt.Errorf("invalid or expired window placement")
	}
	w, h := int64(p.Right)-int64(p.Left), int64(p.Bottom)-int64(p.Top)
	if w < 100 || h < 100 || w > 32768 || h > 32768 ||
		p.Left < -131072 || p.Top < -131072 || p.Right > 131072 || p.Bottom > 131072 {
		return nil, fmt.Errorf("invalid window bounds")
	}
	return &p, nil
}

var captureUpdateWindow = platformCaptureUpdateWindow
var applyUpdateWindow = platformApplyUpdateWindow

func prepareUpdateWindow() func() {
	previous, exists := os.LookupEnv(updateWindowEnv)
	_ = os.Unsetenv(updateWindowEnv)
	p, err := captureUpdateWindow()
	if err != nil {
		log.Printf("update window capture: %v", err)
	} else {
		storeUpdateWindow(p)
	}
	return func() {
		if exists {
			_ = os.Setenv(updateWindowEnv, previous)
		} else {
			_ = os.Unsetenv(updateWindowEnv)
		}
	}
}

func storeUpdateWindow(p *updateWindowPlacement) {
	if p == nil {
		return
	}
	p.Version, p.Expires = 1, time.Now().Add(10*time.Minute).Unix()
	raw, _ := json.Marshal(p)
	if _, err := decodeUpdateWindow(string(raw), time.Now()); err == nil {
		_ = os.Setenv(updateWindowEnv, string(raw))
	}
}

func restoreUpdateWindow() {
	raw := os.Getenv(updateWindowEnv)
	_ = os.Unsetenv(updateWindowEnv)
	if !startedAfterUpdate || raw == "" {
		return
	}
	p, err := decodeUpdateWindow(raw, time.Now())
	if err == nil {
		err = applyUpdateWindow(*p)
	}
	if err != nil {
		log.Printf("update window restore: %v", err)
	}
}
