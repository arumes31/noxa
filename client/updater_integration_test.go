//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"voicx/internal/updatemanifest"
	"voicx/internal/version"
)

const updateIntegrationVersion = "v99.0.0"

// Run with go test -tags=integration -run TestDownloadAndApply ./...
// Every call to the real self-updater runs in a disposable executable copy,
// including rejection tests, so a regression can never replace the test runner.
func TestDownloadAndApplyIntegration(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("real replacement/relaunch exercises the Windows AMD64 client asset")
	}
	original, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	originalBytes, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	payload := buildUpdateProbe(t)
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		wantError  string
		wantBinary bool
		payload    []byte
	}{
		{name: "signed update replaces executable", wantBinary: true},
		{name: "caller asset URLs are revalidated", wantBinary: true},
		{name: "tampered signature", wantError: "signature is not trusted"},
		{name: "tampered manifest", wantError: "signature is not trusted"},
		{name: "wrong manifest version", wantError: "does not match release"},
		{name: "missing trusted key", wantError: "no trusted update signing key"},
		{name: "tampered binary", wantError: "checksum mismatch", wantBinary: true},
		{name: "binary download fails", wantError: "503 Service Unavailable", wantBinary: true},
		{name: "release changes after confirmation", wantError: "confirm the new version"},
		{name: "update withdrawn", wantError: "update is no longer available"},
		{name: "metadata revalidation fails", wantError: "metadata could not be revalidated"},
	}
	for name, invalid := range invalidUpdatePlatforms(t, payload) {
		tests = append(tests, struct {
			name       string
			wantError  string
			wantBinary bool
			payload    []byte
		}{name: name, wantError: "update rejected: invalid Windows AMD64 executable", wantBinary: true, payload: invalid})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := payload
			if tt.payload != nil {
				payload = tt.payload
			}
			dir := t.TempDir()
			executable := filepath.Join(dir, "updater-copy.exe")
			if err := os.WriteFile(executable, originalBytes, 0o700); err != nil {
				t.Fatal(err)
			}
			data := []byte("existing settings and chat history must survive the update\n")
			dataPath := filepath.Join(dir, "user-data.fixture")
			if err := os.WriteFile(dataPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			tempDir := filepath.Join(dir, "downloads")
			if err := os.Mkdir(tempDir, 0o700); err != nil {
				t.Fatal(err)
			}

			manifestVersion := updateIntegrationVersion
			if tt.name == "wrong manifest version" {
				manifestVersion = "v98.0.0"
			}
			manifest := []byte(fmt.Sprintf("%s%s\n%x  %s\n", updatemanifest.VersionPrefix,
				manifestVersion, sha256.Sum256(payload), clientAssetName))
			sig := ed25519.Sign(privateKey, manifest)
			if tt.name == "tampered signature" {
				sig[0] ^= 1
			}
			if tt.name == "tampered manifest" {
				manifest = append(manifest, []byte("unauthenticated extra line\n")...)
			}
			signature := []byte(base64.StdEncoding.EncodeToString(sig))
			var releaseRequests, binaryRequests, untrustedRequests atomic.Int32
			var serverURL string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/releases/latest":
					n := releaseRequests.Add(1)
					tag := updateIntegrationVersion
					if n > 1 {
						switch tt.name {
						case "release changes after confirmation":
							tag = "v100.0.0"
						case "update withdrawn":
							tag = "v0.1.0"
						case "metadata revalidation fails":
							http.Error(w, "unavailable", http.StatusServiceUnavailable)
							return
						}
					}
					assets := []map[string]any{
						{"name": clientAssetName, "browser_download_url": serverURL + "/binary", "size": len(payload)},
						{"name": checksumsName, "browser_download_url": serverURL + "/manifest"},
						{"name": checksumsSignatureName, "browser_download_url": serverURL + "/signature"},
					}
					if err := json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": assets}); err != nil {
						t.Errorf("encode release: %v", err)
					}
				case "/manifest":
					_, _ = w.Write(manifest)
				case "/signature":
					_, _ = w.Write(signature)
				case "/binary":
					binaryRequests.Add(1)
					switch tt.name {
					case "tampered binary":
						_, _ = w.Write([]byte("a substituted executable"))
					case "binary download fails":
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
					default:
						_, _ = w.Write(payload)
					}
				case "/untrusted":
					untrustedRequests.Add(1)
					http.Error(w, "caller-controlled URL must not be fetched", http.StatusBadRequest)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			serverURL = srv.URL
			key := base64.StdEncoding.EncodeToString(publicKey)
			if tt.name == "missing trusted key" {
				key = ""
			}
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestDownloadAndApplyHelperProcess$", "-test.v")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"VOICX_UPDATER_TEST_CHILD="+executable,
				"VOICX_UPDATER_TEST_ORIGINAL="+original,
				"VOICX_UPDATER_TEST_API="+srv.URL,
				"VOICX_UPDATER_TEST_KEY="+key,
				"VOICX_UPDATER_TEST_ERROR="+tt.wantError,
				"VOICX_UPDATER_TEST_CASE="+tt.name,
				"TMP="+tempDir, "TEMP="+tempDir, "TMPDIR="+tempDir)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("isolated update: %v\n%s", err, out)
			}
			got, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			want := originalBytes
			if tt.wantError == "" {
				want = payload
			}
			if !bytes.Equal(got, want) {
				t.Fatal("installed executable differs from expected bytes")
			}
			if gotData, err := os.ReadFile(dataPath); err != nil || !bytes.Equal(gotData, data) {
				t.Fatalf("user data changed: %q, %v", gotData, err)
			}
			if entries, err := os.ReadDir(tempDir); err != nil || len(entries) != 0 {
				t.Fatalf("download temporary files not cleaned up: %v, %v", entries, err)
			}
			if untrustedRequests.Load() != 0 {
				t.Fatal("download followed caller-controlled asset metadata")
			}
			if (binaryRequests.Load() > 0) != tt.wantBinary {
				t.Fatalf("binary requests = %d, want download: %t", binaryRequests.Load(), tt.wantBinary)
			}
		})
	}
	if got, err := os.ReadFile(original); err != nil || !bytes.Equal(got, originalBytes) {
		t.Fatal("primary test executable changed")
	}
}

func TestDownloadAndApplyHelperProcess(t *testing.T) {
	expected := os.Getenv("VOICX_UPDATER_TEST_CHILD")
	if expected == "" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	currentInfo, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(os.Getenv("VOICX_UPDATER_TEST_ORIGINAL"))
	if err != nil {
		t.Fatal(err)
	}
	if executable != expected || os.SameFile(currentInfo, originalInfo) || filepath.Base(executable) != "updater-copy.exe" {
		t.Fatal("refusing to self-update anything except the isolated executable copy")
	}
	version.Version = "0.1.0"
	version.UpdateRepo = "o/r"
	version.UpdatePublicKeys = os.Getenv("VOICX_UPDATER_TEST_KEY")
	updateAPIBase = os.Getenv("VOICX_UPDATER_TEST_API")
	app := &App{}
	info, err := app.CheckForUpdate()
	if err != nil || !info.Available {
		t.Fatalf("initial update check = %+v, %v", info, err)
	}
	if os.Getenv("VOICX_UPDATER_TEST_CASE") == "caller asset URLs are revalidated" {
		info.URL = updateAPIBase + "/untrusted"
		info.SHA256URL = info.URL
		info.SignatureURL = info.URL
		info.Size = 1
	}
	got := app.DownloadAndApply(info)
	wantError := os.Getenv("VOICX_UPDATER_TEST_ERROR")
	if wantError == "" && got != "" || wantError != "" && !strings.Contains(got, wantError) {
		t.Fatalf("DownloadAndApply = %q, want error containing %q", got, wantError)
	}
	if wantError == "" {
		// Relaunch before this old process exits, just like ApplyAndRestart.
		// In particular, Windows must allow the replacement to execute while
		// the renamed old executable is still mapped by this process.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		probe := exec.CommandContext(ctx, executable)
		if out, err := probe.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != updateIntegrationVersion {
			t.Fatalf("updated executable version probe = %q, %v", out, err)
		}
	}
}

func buildUpdateProbe(t *testing.T) []byte {
	return buildUpdateProbeFor(t, "windows", "amd64")
}

func buildUpdateProbeFor(t *testing.T, goos, goarch string) []byte {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	code := "package main\nimport \"fmt\"\nfunc main() { fmt.Println(\"" + updateIntegrationVersion + "\") }\n"
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "version-probe.exe")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", executable, source)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build replacement version probe: %v\n%s", err, out)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
