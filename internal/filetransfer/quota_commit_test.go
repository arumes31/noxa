package filetransfer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"noxa/internal/store"
)

func TestPendingUploadsRecheckQuotaBeforeCommit(t *testing.T) {
	for _, axis := range []string{"channel", "uploader", "server_uploader"} {
		for _, identical := range []bool{false, true} {
			name := axis + "/different_content"
			if identical {
				name = axis + "/deduplicated_content"
			}
			t.Run(name, func(t *testing.T) {
				fs := newFakeFileStore()
				cfg := Config{Addr: ":0", RootDir: t.TempDir()}
				var uploaderLimit int64
				wantErr := ErrQuotaExceeded
				if axis == "channel" {
					cfg.ChannelQuotaMB = 1
				} else {
					uploaderLimit = 1
					wantErr = ErrUploaderQuotaExceeded
				}
				if axis == "server_uploader" {
					uploaderLimit = 0
					cfg.UserQuotaMB = 1
				}
				s := New(cfg, fs, nil)
				first := bytes.Repeat([]byte("a"), 700*1024)
				second := bytes.Repeat([]byte("b"), len(first))
				if identical {
					second = first
				}
				// Both bearers are issued while usage is zero.
				pending := make([]*transfer, 2)
				for i, file := range []string{"first.bin", "second.bin"} {
					id, token, err := s.InitUpload(t.Context(), 7, "", file, int64(len(first)), "member", uploaderLimit)
					if err != nil {
						t.Fatal(err)
					}
					pending[i], err = s.consume(token, id)
					if err != nil {
						t.Fatal(err)
					}
				}
				root, err := s.openBlobRoot()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = root.Close() }()
				if err := root.MkdirAll("7", 0o700); err != nil {
					t.Fatal(err)
				}
				for i, data := range [][]byte{first, second} {
					tr := pending[i]
					final := blobPath(7, "", tr.Name)
					tmp := final + ".part"
					if err := os.WriteFile(filepath.Join(cfg.RootDir, tmp), data, 0o600); err != nil {
						t.Fatal(err)
					}
					err := s.finalizeUpload(t.Context(), tr, root, tmp, final, int64(len(data)), sha256Hex(data))
					if i == 0 || identical {
						if err != nil {
							t.Fatalf("commit %d: %v", i, err)
						}
					} else {
						if !errors.Is(err, wantErr) {
							t.Fatalf("second commit = %v, want %v", err, wantErr)
						}
						if _, err := root.Stat(final); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("denied upload was published: %v", err)
						}
						if _, err := fs.GetFile(t.Context(), 7, "", tr.Name); !errors.Is(err, store.ErrFileNotFound) {
							t.Fatalf("denied upload was recorded: %v", err)
						}
					}
				}
				usage, err := fs.ChannelFileUsage(t.Context(), 7)
				if err != nil || usage != int64(len(first)) {
					t.Fatalf("final usage = %d, %v", usage, err)
				}
			})
		}
	}
}

type failingContentQuotaStore struct{ *fakeFileStore }

func (s failingContentQuotaStore) FileContentUsage(context.Context, int64, string, string) (int64, int64, error) {
	return 0, 0, errors.New("quota database unavailable")
}

func TestCommitQuotaFailurePreservesExistingVersions(t *testing.T) {
	for _, scenario := range []string{"other_channel", "other_uploader", "usage_error"} {
		t.Run(scenario, func(t *testing.T) {
			fs := newFakeFileStore()
			s := New(Config{Addr: ":0", RootDir: t.TempDir()}, fs, nil)
			data := bytes.Repeat([]byte("a"), 700*1024)
			id, token, err := s.InitUpload(t.Context(), 7, "", "file.bin", int64(len(data)), "member", 1)
			if err != nil {
				t.Fatal(err)
			}
			tr, err := s.consume(token, id)
			if err != nil {
				t.Fatal(err)
			}
			root, err := s.openBlobRoot()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			if err := root.MkdirAll("7", 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"file.bin", "file.bin.v1", "file.bin.v3"} {
				if err := os.WriteFile(filepath.Join(s.cfg.RootDir, "7", name), []byte(name), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := fs.AddFile(t.Context(), store.FileRecord{ChannelID: 7, Name: name, SHA256: sha256Hex([]byte(name)), Size: int64(len(name)), Uploader: "member"}); err != nil {
					t.Fatal(err)
				}
			}
			// Same bytes in a different channel or owned only by another uploader
			// cannot be discounted from this uploader's added quota usage.
			if err := fs.AddFile(t.Context(), store.FileRecord{ChannelID: 8, Name: "elsewhere", SHA256: sha256Hex(data), Size: int64(len(data)), Uploader: "member"}); err != nil {
				t.Fatal(err)
			}
			if scenario == "other_uploader" {
				if err := fs.AddFile(t.Context(), store.FileRecord{ChannelID: 7, Name: "theirs", SHA256: sha256Hex(data), Size: int64(len(data)), Uploader: "other"}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "usage_error" {
				s.store = failingContentQuotaStore{fs}
			}
			final := blobPath(7, "", "file.bin")
			tmp := final + ".part"
			if err := os.WriteFile(filepath.Join(s.cfg.RootDir, tmp), data, 0o600); err != nil {
				t.Fatal(err)
			}
			err = s.finalizeUpload(t.Context(), tr, root, tmp, final, int64(len(data)), sha256Hex(data))
			if err == nil || (scenario != "usage_error" && !errors.Is(err, ErrUploaderQuotaExceeded)) {
				t.Fatalf("commit error = %v", err)
			}
			for _, name := range []string{"file.bin", "file.bin.v1", "file.bin.v3"} {
				got, err := os.ReadFile(filepath.Join(s.cfg.RootDir, "7", name))
				if err != nil || string(got) != name {
					t.Fatalf("%s changed: %q, %v", name, got, err)
				}
				rec, err := fs.GetFile(t.Context(), 7, "", name)
				if err != nil || rec.SHA256 != sha256Hex([]byte(name)) {
					t.Fatalf("%s metadata changed: %+v, %v", name, rec, err)
				}
			}
		})
	}
}

func TestQuotaExceededDoesNotOverflow(t *testing.T) {
	if !(Quota{Used: 1, Limit: 1 << 20}).Exceeded(1<<63 - 1) {
		t.Fatal("overflow bypassed quota")
	}
}
