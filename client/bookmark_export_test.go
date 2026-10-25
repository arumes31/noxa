package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func TestPrepareBookmarkExportStripsCredentialsByDefault(t *testing.T) {
	input := `{"format":"noxa-bookmarks","version":1,"bookmarks":[{"name":"Home","addr":"voice.example:12333","nickname":"dan","password":"hunter2","server_password":"x","token":"abc","nested":{"api_key":"k","color":"#fff"},"pw":"p","spw":"s"}]}`
	raw, err := prepareBookmarkExport(input, false, func(string, string) (loginPasswords, error) {
		t.Fatal("lookup must not run without opt-in")
		return loginPasswords{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"hunter2", "\"password\"", "server_password", "\"token\"", "api_key", "\"pw\"", "\"spw\""} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("stripped export still contains %q:\n%s", forbidden, text)
		}
	}
	for _, kept := range []string{"\"Home\"", "voice.example:12333", "\"dan\"", "#fff", "\"include_credentials\": false"} {
		if !strings.Contains(text, kept) {
			t.Fatalf("stripped export lost %q:\n%s", kept, text)
		}
	}
}

func TestPrepareBookmarkExportAttachesSavedPasswordsOnlyWhenRequested(t *testing.T) {
	input := `{"format":"noxa-bookmarks","version":1,"bookmarks":[{"name":"A","addr":"a.example","nickname":"dan"},{"name":"B","addr":"b.example","nickname":"dan"},{"name":"guest","addr":"c.example"}]}`
	raw, err := prepareBookmarkExport(input, true, func(addr, nickname string) (loginPasswords, error) {
		switch addr {
		case "a.example":
			return loginPasswords{Password: "acct", ServerPassword: "srv"}, nil
		case "b.example":
			return loginPasswords{}, errors.New("store locked")
		}
		t.Fatalf("unexpected lookup for %q/%q", addr, nickname)
		return loginPasswords{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		IncludeCredentials bool             `json:"include_credentials"`
		Bookmarks          []map[string]any `json:"bookmarks"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.IncludeCredentials || len(payload.Bookmarks) != 3 {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Bookmarks[0]["password"] != "acct" || payload.Bookmarks[0]["server_password"] != "srv" {
		t.Fatalf("bookmark A lacks saved passwords: %v", payload.Bookmarks[0])
	}
	if _, ok := payload.Bookmarks[1]["password"]; ok {
		t.Fatalf("bookmark B must not carry a password when the store failed: %v", payload.Bookmarks[1])
	}
	if _, ok := payload.Bookmarks[2]["password"]; ok {
		t.Fatalf("guest bookmark without nickname must not be looked up: %v", payload.Bookmarks[2])
	}
}

func TestPrepareBookmarkExportRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{"", "[]", "{", `{"bookmarks":"nope"}`, strings.Repeat("{", maxBookmarkExportBytes+1)} {
		if _, err := prepareBookmarkExport(input, false, nil); err == nil {
			t.Fatalf("input %.20q accepted", input)
		}
	}
}

func TestSaveBookmarkExportOutcomes(t *testing.T) {
	input := `{"bookmarks":[{"name":"A","addr":"a","nickname":"n","password":"p"}]}`
	cancelled := func(context.Context, wailsRuntime.SaveDialogOptions) (string, error) { return "", nil }
	chosen := func(_ context.Context, options wailsRuntime.SaveDialogOptions) (string, error) {
		if options.DefaultFilename != "noxa-bookmarks.json" {
			t.Fatalf("default filename = %q", options.DefaultFilename)
		}
		return "C:/tmp/out.json", nil
	}
	var written []byte
	write := func(_ string, raw []byte) error { written = raw; return nil }
	if got := saveBookmarkExport(context.Background(), input, false, nil, cancelled, write); got != "cancelled" {
		t.Fatalf("cancelled dialog = %q", got)
	}
	if got := saveBookmarkExport(context.Background(), input, false, nil, chosen, write); got != "saved" {
		t.Fatalf("saved = %q", got)
	}
	if strings.Contains(string(written), "\"p\"") {
		t.Fatalf("default export wrote a password: %s", written)
	}
	if got := saveBookmarkExport(context.Background(), "nope", false, nil, chosen, write); got != "error" {
		t.Fatalf("invalid input = %q", got)
	}
	if got := saveBookmarkExport(context.Background(), input, false, nil, chosen, func(string, []byte) error { return errors.New("disk full") }); got != "error" {
		t.Fatalf("write failure = %q", got)
	}
}
