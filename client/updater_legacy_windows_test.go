//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"noxa/internal/updatemanifest"
)

type legacyReleaseFixture struct {
	Version       string                         `json:"version"`
	Commit        string                         `json:"commit"`
	SHA256        string                         `json:"sha256"`
	Manifest      string                         `json:"manifest"`
	Signature     string                         `json:"signature"`
	BrowserPolicy edge.UpdateCompatibilityPolicy `json:"browser_policy"`
}

func verifiedLegacyReleaseFixtures(t *testing.T) []legacyReleaseFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/legacy-update-releases.json")
	if err != nil {
		t.Fatal(err)
	}
	var releases []legacyReleaseFixture
	if err := json.Unmarshal(data, &releases); err != nil {
		t.Fatal(err)
	}
	if len(releases) != 64 {
		t.Fatalf("expected every published release v0.5.24 through v0.5.87; got %d", len(releases))
	}
	// Independently pinned release key, not supplied by a fixture or live server.
	const key = "V2gJha/+dbSOAz0F8EBCX6358VJJD0JDtD+wKZeemFg="
	for i, release := range releases {
		if want := fmt.Sprintf("v0.5.%d", i+24); release.Version != want {
			t.Fatalf("missing release %s", want)
		}
		if err := updatemanifest.Verify([]byte(release.Manifest), []byte(release.Signature), key, release.Version); err != nil {
			t.Fatalf("%s: %v", release.Version, err)
		}
		if !strings.Contains(release.Manifest, "\n"+release.SHA256+"  noxa-client-windows-amd64.exe\n") {
			t.Fatalf("%s: digest absent from signed manifest", release.Version)
		}
	}
	return releases
}

func TestUpdateHandoffAllPublishedLegacyReleases(t *testing.T) {
	for _, release := range verifiedLegacyReleaseFixtures(t) {
		t.Run(release.Version, func(t *testing.T) {
			decoded, err := hex.DecodeString(release.SHA256)
			if err != nil || len(decoded) != sha256.Size {
				t.Fatal("invalid published digest")
			}
			digest := [sha256.Size]byte(decoded)
			name, policy := legacyUpdateReleasePolicy(digest)
			if name != release.Version || policy != release.BrowserPolicy || !legacyUpdateHash(digest) {
				t.Fatal("authenticated legacy release cannot use update bridge")
			}
			digest[0] ^= 1
			if legacyUpdateHash(digest) {
				t.Fatal("modified executable accepted for legacy bridge")
			}
		})
	}
	if legacyUpdateHash([sha256.Size]byte{}) {
		t.Fatal("unknown executable accepted")
	}
}

func TestUpdateHandoffRejectsDifferentAuthenticatedLegacyParent(t *testing.T) {
	releases := verifiedLegacyReleaseFixtures(t)
	for _, parent := range releases {
		parentBytes, _ := hex.DecodeString(parent.SHA256)
		parentHash := [sha256.Size]byte(parentBytes)
		for _, snapshot := range releases {
			snapshotBytes, _ := hex.DecodeString(snapshot.SHA256)
			snapshotHash := [sha256.Size]byte(snapshotBytes)
			if got, want := legacyUpdateParentHash(parentHash, snapshotHash), parent.Version == snapshot.Version; got != want {
				t.Fatalf("parent %s with rollback %s: got %v want %v", parent.Version, snapshot.Version, got, want)
			}
		}
	}
	unknown := sha256.Sum256([]byte("unpublished executable"))
	if legacyUpdateParentHash(unknown, unknown) {
		t.Fatal("matching unknown parent and snapshot accepted")
	}
}
