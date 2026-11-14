package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"noxa/internal/updatepatch"
	"noxa/internal/version"
)

type patchFixture struct {
	oldFile, newFile, patch []byte
	full, patched           atomic.Int32
	srv                     *httptest.Server
}

func newPatchFixture(t *testing.T) *patchFixture {
	t.Helper()
	oldVersion := version.Version
	version.Version = "1.0.0"
	t.Cleanup(func() { version.Version = oldVersion })

	rng := rand.New(rand.NewPCG(3, 4))
	f := &patchFixture{oldFile: make([]byte, 256<<10)}
	for i := range f.oldFile {
		f.oldFile[i] = byte(rng.Uint32())
	}
	f.newFile = append(append([]byte(nil), f.oldFile...), "new release"...)
	var err error
	if f.patch, err = updatepatch.Create(f.oldFile, f.newFile); err != nil {
		t.Fatal(err)
	}

	exe := filepath.Join(t.TempDir(), "noxa.exe")
	if err := os.WriteFile(exe, f.oldFile, 0o600); err != nil {
		t.Fatal(err)
	}
	oldExecutable := currentExecutable
	currentExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { currentExecutable = oldExecutable })

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/full":
			f.full.Add(1)
			_, _ = w.Write(f.newFile)
		case "/patch":
			f.patched.Add(1)
			_, _ = w.Write(f.patch)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	withUpdateAPIBase(t, f.srv.URL)
	return f
}

func (f *patchFixture) info() UpdateInfo {
	return UpdateInfo{URL: f.srv.URL + "/full", patchURL: f.srv.URL + "/patch", Size: int64(len(f.newFile))}
}

func (f *patchFixture) sums(patchSum, clientSum []byte) string {
	return fmt.Sprintf("# noxa-version: v2.0.0\n%x  %s\n%x  %s\n",
		clientSum, clientAssetName, patchSum, clientPatchPrefix+"1.0.0.patch")
}

func digest(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

// run downloads into a fresh directory and checks the verified result.
func (f *patchFixture) run(t *testing.T, info UpdateInfo, sums string) {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	var progress int
	if err := downloadClientAsset(t.Context(), root, info, sums, func(updateProgress) { progress++ }); err != nil {
		t.Fatalf("downloadClientAsset: %v", err)
	}
	if got, err := root.ReadFile(clientAssetName); err != nil || !bytes.Equal(got, f.newFile) {
		t.Fatalf("client asset differs from the new release (%v)", err)
	}
	if progress == 0 {
		t.Fatal("no progress reported")
	}
}

func TestDownloadClientAssetUsesSignedPatch(t *testing.T) {
	f := newPatchFixture(t)
	f.run(t, f.info(), f.sums(digest(f.patch), digest(f.newFile)))
	if f.patched.Load() != 1 || f.full.Load() != 0 {
		t.Fatalf("patch requests %d, full requests %d; want patch only", f.patched.Load(), f.full.Load())
	}
}

func TestDownloadClientAssetFallsBackToFullClient(t *testing.T) {
	for _, tc := range []struct {
		name      string
		setup     func(*testing.T, *patchFixture) (UpdateInfo, string)
		wantPatch bool
	}{
		{"no patch asset", func(_ *testing.T, f *patchFixture) (UpdateInfo, string) {
			info := f.info()
			info.patchURL = ""
			return info, f.sums(digest(f.patch), digest(f.newFile))
		}, false},
		{"patch not in signed manifest", func(_ *testing.T, f *patchFixture) (UpdateInfo, string) {
			return f.info(), fmt.Sprintf("# noxa-version: v2.0.0\n%x  %s\n", digest(f.newFile), clientAssetName)
		}, false},
		{"patch checksum mismatch", func(_ *testing.T, f *patchFixture) (UpdateInfo, string) {
			return f.info(), f.sums(digest([]byte("other")), digest(f.newFile))
		}, true},
		{"corrupt patch", func(_ *testing.T, f *patchFixture) (UpdateInfo, string) {
			f.patch = append([]byte(nil), f.patch...)
			f.patch[len(f.patch)/2] ^= 0xff
			return f.info(), f.sums(digest(f.patch), digest(f.newFile))
		}, true},
		{"patch output does not match signed client", func(t *testing.T, f *patchFixture) (UpdateInfo, string) {
			other, err := updatepatch.Create(f.oldFile, append(append([]byte(nil), f.oldFile...), "unsigned build"...))
			if err != nil {
				t.Fatal(err)
			}
			f.patch = other
			return f.info(), f.sums(digest(f.patch), digest(f.newFile))
		}, true},
		{"patch larger than the full client", func(_ *testing.T, f *patchFixture) (UpdateInfo, string) {
			info := f.info()
			info.Size = int64(len(f.patch)) - 1
			return info, f.sums(digest(f.patch), digest(f.newFile))
		}, true},
		{"running executable unreadable", func(t *testing.T, f *patchFixture) (UpdateInfo, string) {
			missing := filepath.Join(t.TempDir(), "missing-noxa.exe")
			currentExecutable = func() (string, error) { return missing, nil }
			return f.info(), f.sums(digest(f.patch), digest(f.newFile))
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPatchFixture(t)
			info, sums := tc.setup(t, f)
			f.run(t, info, sums)
			if f.full.Load() != 1 || (f.patched.Load() == 1) != tc.wantPatch {
				t.Fatalf("patch requests %d, full requests %d", f.patched.Load(), f.full.Load())
			}
		})
	}
}

func TestCheckForUpdateFindsPatchForRunningVersion(t *testing.T) {
	withUpdateRepo(t, "o/r")
	oldVersion := version.Version
	version.Version = "1.0.0"
	defer func() { version.Version = oldVersion }()
	release := func(patchURL string) string {
		return `{"tag_name": "v2.0.0", "assets": [
			{"name": "noxa-client-windows-amd64.exe", "browser_download_url": "https://github.com/o/r/full.exe", "size": 10},
			{"name": "checksums.txt", "browser_download_url": "https://github.com/o/r/checksums.txt"},
			{"name": "checksums.txt.sig", "browser_download_url": "https://github.com/o/r/checksums.txt.sig"},
			{"name": "noxa-client-windows-amd64-from-0.9.0.patch", "browser_download_url": "https://github.com/o/r/other.patch"},
			{"name": "noxa-client-windows-amd64-from-1.0.0.patch", "browser_download_url": "` + patchURL + `"}]}`
	}
	for patchURL, want := range map[string]string{
		"https://github.com/o/r/mine.patch": "https://github.com/o/r/mine.patch",
		"http://example.com/mine.patch":     "", // unsafe URL: ignored, full update still offered
	} {
		srv := fakeGitHub(t, release(patchURL))
		withUpdateAPIBase(t, srv.URL)
		info, err := (&App{}).CheckForUpdate()
		srv.Close()
		if err != nil || !info.Available || info.patchURL != want {
			t.Fatalf("CheckForUpdate(%s) = %+v, %v; want patch %q", patchURL, info, err, want)
		}
	}
}
