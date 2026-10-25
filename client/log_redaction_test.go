package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactLogSecretsMasksPairingMaterial(t *testing.T) {
	link := "https://chat.example.test/link#id=QmFzZTY0VXJsSWQ&k=c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0"
	cases := map[string]string{
		"device link " + link + " created":                 "device link https://chat.example.test/link#[redacted] created",
		"typed code 7KQ3-PX2M for the phone":              "typed code [redacted] for the phone",
		"resolution 2345-6789 stays":                      "resolution 2345-6789 stays",
		`result {"link_id":"abc","secret":"c2Vj","code":"7KQ3-PX2M","qr":"data:image/png;base64,AAAA"}`: `result {"link_id":"abc","secret":"[redacted]","code":"[redacted]","qr":"[redacted]"}`,
		"https://x.test/app/?token=abc123&page=2":         "https://x.test/app/?token=[redacted]&page=2",
		"img data:image/png;base64," + strings.Repeat("Q", 100): "img data:image/png;base64,[redacted]",
		"avatar override upload failed: timeout":          "avatar override upload failed: timeout",
	}
	for in, want := range cases {
		if got := string(redactLogSecrets([]byte(in))); got != want {
			t.Errorf("redactLogSecrets(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDailyLogWriterRedactsPairingSecrets(t *testing.T) {
	dir := t.TempDir()
	w, err := newDailyLogWriter(dir, "client.log")
	if err != nil {
		t.Fatal(err)
	}
	line := "pairing https://chat.example.test/link#id=abc&k=secret code 7KQ3-PX2M\n"
	n, err := w.Write([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(line) {
		t.Fatalf("n = %d, want %d (io.Writer contract reports the caller's bytes)", n, len(line))
	}
	w.Close()
	data, err := os.ReadFile(filepath.Join(dir, "client.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "k=secret") || strings.Contains(string(data), "7KQ3-PX2M") {
		t.Fatalf("log kept pairing secrets: %q", data)
	}
	if !strings.Contains(string(data), "link#[redacted]") {
		t.Fatalf("log line not redacted: %q", data)
	}
}
