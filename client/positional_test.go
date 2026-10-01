package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPositionInputIsOptInBoundedAndFresh(t *testing.T) {
	app := NewApp()
	app.settingsPath = filepath.Join(t.TempDir(), "settings.json")
	path := app.PositionalInputPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	valid := `{"x":3,"y":1,"z":-2,"context":"test-map","forward":[0,0,-1],"up":[0,1,0]}`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ReadPositionalInput(); err == nil {
		t.Fatal("disabled input was read")
	}
	app.settings.PositionalAudio = true
	got, err := app.ReadPositionalInput()
	if err != nil || got.X != 3 || got.Context != "test-map" {
		t.Fatalf("read=%+v error=%v", got, err)
	}
	past := time.Now().Add(-5 * time.Second)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ReadPositionalInput(); err == nil {
		t.Fatal("stale input accepted")
	}
	for _, bad := range []string{`{"x":1e100}`, `{"x":0,"context":"map","forward":[0,0,0],"up":[0,1,0]}`, `null`, string(make([]byte, 4097))} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := app.ReadPositionalInput(); err == nil {
			t.Fatalf("invalid input accepted: %.40s", bad)
		}
	}
}
