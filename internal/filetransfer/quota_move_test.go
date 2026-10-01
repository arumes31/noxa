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

func TestCrossChannelMoveHonorsResourceQuotas(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		channelLimit, uploaderLimit             int64
		sourceCopy, targetCopy, targetDifferent bool
		wantErr                                 error
	}{
		{name: "channel_full", channelLimit: 1, targetDifferent: true, wantErr: ErrQuotaExceeded},
		{name: "channel_dedup", channelLimit: 1, targetCopy: true},
		{name: "uploader_source_copy", uploaderLimit: 1, sourceCopy: true, wantErr: ErrUploaderQuotaExceeded},
		{name: "uploader_relocation", uploaderLimit: 1},
		{name: "uploader_consolidation", uploaderLimit: 1, targetCopy: true},
		{name: "uploader_target_copy", uploaderLimit: 2, sourceCopy: true, targetCopy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeFileStore()
			s := New(Config{Addr: ":0", RootDir: t.TempDir(), ChannelQuotaMB: tc.channelLimit, UserQuotaMB: tc.uploaderLimit}, fs, nil)
			payload := bytes.Repeat([]byte("a"), 700*1024)
			seed := func(channel int64, name string, data []byte) {
				t.Helper()
				path := s.filePath(channel, "", name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := fs.AddFile(t.Context(), store.FileRecord{ChannelID: channel, Name: name, Size: int64(len(data)), SHA256: sha256Hex(data), Uploader: "original-uploader"}); err != nil {
					t.Fatal(err)
				}
			}
			seed(7, "move.bin", payload)
			if tc.sourceCopy {
				seed(7, "copy.bin", payload)
			}
			if tc.targetCopy {
				seed(8, "target-copy.bin", payload)
			}
			if tc.targetDifferent {
				seed(8, "other.bin", bytes.Repeat([]byte("b"), len(payload)))
			}
			err := s.MoveFile(t.Context(), 7, "", "move.bin", 8, "", "moved.bin")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("move = %v, want %v", err, tc.wantErr)
			}
			channel, name := int64(8), "moved.bin"
			if tc.wantErr != nil {
				channel, name = 7, "move.bin"
			}
			got, err := os.ReadFile(s.filePath(channel, "", name))
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("expected blob missing/changed: %v", err)
			}
			rec, err := fs.GetFile(t.Context(), channel, "", name)
			if err != nil || rec.Uploader != "original-uploader" {
				t.Fatalf("metadata/uploader changed: %+v, %v", rec, err)
			}
			if tc.wantErr != nil {
				if _, err := fs.GetFile(t.Context(), 8, "", "moved.bin"); !errors.Is(err, store.ErrFileNotFound) {
					t.Fatalf("denied target metadata exists: %v", err)
				}
				if _, err := os.Stat(s.filePath(8, "", "moved.bin")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("denied target blob exists: %v", err)
				}
			}
		})
	}
}

type targetReadErrorStore struct{ *fakeFileStore }

func (s targetReadErrorStore) GetFile(ctx context.Context, channel int64, folder, name string) (*store.FileRecord, error) {
	if channel == 8 {
		return nil, errors.New("target lookup unavailable")
	}
	return s.fakeFileStore.GetFile(ctx, channel, folder, name)
}

func TestMoveQuotaLookupFailurePreservesSource(t *testing.T) {
	for _, lookup := range []string{"quota", "target"} {
		t.Run(lookup, func(t *testing.T) {
			fs := newFakeFileStore()
			var backend FileStore = failingContentQuotaStore{fs}
			if lookup == "target" {
				backend = targetReadErrorStore{fs}
			}
			s := New(Config{Addr: ":0", RootDir: t.TempDir(), UserQuotaMB: 1}, backend, nil)
			if err := fs.AddFile(t.Context(), store.FileRecord{ChannelID: 7, Name: "source", Size: 7, SHA256: "hash", Uploader: "member"}); err != nil {
				t.Fatal(err)
			}
			path := s.filePath(7, "", "source")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := s.MoveFile(t.Context(), 7, "", "source", 8, "", "target"); err == nil {
				t.Fatal("move ignored lookup failure")
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != "payload" {
				t.Fatalf("source changed: %q, %v", got, err)
			}
			if _, err := fs.GetFile(t.Context(), 7, "", "source"); err != nil {
				t.Fatal(err)
			}
			if _, err := fs.GetFile(t.Context(), 8, "", "target"); !errors.Is(err, store.ErrFileNotFound) {
				t.Fatalf("target metadata written: %v", err)
			}
			if _, err := os.Stat(filepath.Join(s.cfg.RootDir, "8")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target directory created: %v", err)
			}
		})
	}
}

func TestConfiguredUploaderQuotaCannotBeBypassedAtIssuance(t *testing.T) {
	for _, supplied := range []int64{0, 10} {
		s := New(Config{Addr: ":0", RootDir: t.TempDir(), UserQuotaMB: 1}, newFakeFileStore(), nil)
		if _, _, err := s.InitUpload(t.Context(), 7, "", "too-big.bin", 2<<20, "member", supplied); !errors.Is(err, ErrUploaderQuotaExceeded) {
			t.Fatalf("supplied limit %d bypassed server quota: %v", supplied, err)
		}
	}
}
