package defaultavatars

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"golang.org/x/image/webp"
)

func TestPortraitPool(t *testing.T) {
	entries, err := portraits.ReadDir("portraits")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 100 {
		t.Fatalf("portrait count = %d, want 100", len(entries))
	}
	seen := make(map[[32]byte]string)
	total := 0
	for n := 1; n <= 100; n++ {
		name := fmt.Sprintf("portraits/%03d.webp", n)
		raw, err := portraits.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 64*1024 {
			t.Errorf("%s exceeds 64 KiB", name)
		}
		total += len(raw)
		img, err := webp.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
			t.Errorf("%s: dimensions %v", name, img.Bounds())
		}
		digest := sha256.Sum256(raw)
		if prior, ok := seen[digest]; ok {
			t.Errorf("duplicate portraits: %s and %s", prior, name)
		}
		seen[digest] = name
	}
	if total > 2*1024*1024 {
		t.Errorf("portrait pool exceeds 2 MiB: %d", total)
	}
}

func TestIdentityAssignmentIsStable(t *testing.T) {
	want, err := portraits.ReadFile("portraits/033.webp")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		got, err := Get("user-uid")
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("v1 assignment changed: %v", err)
		}
	}
	if _, err := Get(""); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("empty identity: %v", err)
	}
	// Arbitrary identifiers select embedded images, never filesystem paths.
	for _, uid := range []string{"../outside", "a/b", "identity-with-unicode-雪"} {
		got, err := Get(uid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := webp.DecodeConfig(bytes.NewReader(got)); err != nil {
			t.Fatal(err)
		}
	}
}
