package main

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"noxa/internal/netproto"
)

type PositionalInput struct {
	netproto.PositionUpdate
	Forward [3]float64 `json:"forward"`
	Up      [3]float64 `json:"up"`
}

func (a *App) PositionalInputPath() string {
	path := a.settingsFile()
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), "positional", "input.json")
}

// ReadPositionalInput accepts only fresh bounded local game data, never arbitrary
// paths supplied by a server. Input is optional; missing data means normal audio.
func (a *App) ReadPositionalInput() (PositionalInput, error) {
	var input PositionalInput
	a.settingsMu.Lock()
	enabled := a.settings.PositionalAudio
	a.settingsMu.Unlock()
	if !enabled {
		return input, errors.New("positional audio is disabled")
	}
	path := a.PositionalInputPath()
	if path == "" {
		return input, errors.New("positional input unavailable")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return input, errors.New("no positional input")
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(filepath.Base(path))
	if err != nil {
		return input, errors.New("no positional input")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || time.Since(info.ModTime()) > 2*time.Second || info.ModTime().After(time.Now().Add(time.Second)) {
		return input, errors.New("positional input is stale or invalid")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return input, errors.New("invalid positional input")
	}
	if err := json.Unmarshal(data, &input); err != nil || !input.ValidPosition() {
		return input, errors.New("invalid positional input")
	}
	var frontLength, upLength, dot float64
	for i := range input.Forward {
		f, u := input.Forward[i], input.Up[i]
		if math.IsNaN(f) || math.IsInf(f, 0) || math.IsNaN(u) || math.IsInf(u, 0) {
			return input, errors.New("invalid listener orientation")
		}
		frontLength += f * f
		upLength += u * u
		dot += f * u
	}
	if frontLength < 0.5 || frontLength > 1.5 || upLength < 0.5 || upLength > 1.5 || math.Abs(dot) > 0.1 {
		return input, errors.New("listener orientation must use orthogonal unit vectors")
	}
	return input, nil
}

func (a *App) PublishPositionForTab(tabID string, position netproto.PositionUpdate) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	a.settingsMu.Lock()
	enabled := a.settings.PositionalAudio
	a.settingsMu.Unlock()
	if !enabled || position.ChannelID <= 0 || !position.ValidPosition() {
		return "invalid or disabled positional input"
	}
	if err := cm.write(netproto.MsgPositionUpdate, position); err != nil {
		return err.Error()
	}
	return ""
}
