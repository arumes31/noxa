// files_test.go DB-backed tests for the wave-7 file metadata (folders,
// rename/move, versions, dedup lookup). Skip pattern like the other store
// tests: without a reachable Postgres they skip.
package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestFileGlobalScopeDB(t *testing.T) {
	for _, scenario := range []string{"fresh", "upgrade"} {
		t.Run(scenario, func(t *testing.T) {
			s := testScratchStore(t)
			ctx := t.Context()
			if scenario == "upgrade" {
				applyPreLedgerMigrations(t, s, "037")
				if _, err := s.DB().ExecContext(ctx, `CREATE TABLE schema_migrations (
					filename TEXT PRIMARY KEY,
					checksum TEXT NOT NULL,
					applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
					CONSTRAINT schema_migrations_checksum_sha256 CHECK (checksum ~ '^[0-9a-f]{64}$')
				)`); err != nil {
					t.Fatal(err)
				}
				for _, name := range migrationNames(t) {
					if name >= "037" {
						break
					}
					if _, err := s.DB().ExecContext(ctx, `INSERT INTO schema_migrations (filename, checksum)
						VALUES ($1, $2)`, name, migrationChecksum(t, name)); err != nil {
						t.Fatal(err)
					}
				}
				var foreignKey *pq.Error
				err := s.AddFile(ctx, FileRecord{ChannelID: 0, Name: "before.noxac", SHA256: "before"})
				if !errors.As(err, &foreignKey) || foreignKey.Code != "23503" {
					t.Fatalf("old schema did not reject scope zero: %v", err)
				}
			} else if err := s.Migrate(); err != nil {
				t.Fatal(err)
			}
			var channelID int64
			if err := s.DB().QueryRowContext(ctx, `INSERT INTO channels (name, channel_type) VALUES ('file-scope-test', 2) RETURNING id`).Scan(&channelID); err != nil {
				t.Fatal(err)
			}
			if err := s.AddFile(ctx, FileRecord{ChannelID: channelID, Name: "existing.txt", SHA256: "existing", Size: 11}); err != nil {
				t.Fatal(err)
			}
			if err := s.Migrate(); err != nil {
				t.Fatal(err)
			}
			if existing, err := s.GetFile(ctx, channelID, "", "existing.txt"); err != nil || existing.Size != 11 {
				t.Fatalf("existing channel file after migration = %+v, %v", existing, err)
			}

			global := FileRecord{ChannelID: 0, Folder: "attachments", Name: "voice.noxac", Size: 10, SHA256: "initial", Uploader: "scope-test", Encrypted: true}
			if err := s.AddFile(ctx, global); err != nil {
				t.Fatalf("add global attachment: %v", err)
			}
			global.Size, global.SHA256 = 20, "replacement"
			if err := s.AddFile(ctx, global); err != nil {
				t.Fatalf("replace global attachment: %v", err)
			}
			files, err := s.ListFiles(ctx, 0, "attachments")
			if err != nil || len(files) != 1 || files[0].ChannelID != 0 || files[0].Size != 20 || !files[0].Encrypted {
				t.Fatalf("global upsert = %+v, %v", files, err)
			}
			copy := global
			copy.Folder = "copies"
			if err := s.AddFile(ctx, copy); err != nil {
				t.Fatal(err)
			}
			folders, err := s.ListFileFolders(ctx, 0)
			if err != nil || len(folders) != 2 || folders[0] != "attachments" || folders[1] != "copies" {
				t.Fatalf("global folders = %v, %v", folders, err)
			}
			if found, err := s.FindFileBySHA(ctx, 0, global.SHA256, global.Folder, global.Name); err != nil || found == nil || found.Folder != "copies" {
				t.Fatalf("global dedup = %+v, %v", found, err)
			}
			if used, err := s.ChannelFileUsage(ctx, 0); err != nil || used != 20 {
				t.Fatalf("global quota = %d, %v", used, err)
			}
			if channel, uploader, err := s.FileContentUsage(ctx, 0, global.SHA256, global.Uploader); err != nil || channel != 20 || uploader != 20 {
				t.Fatalf("global content usage = %d/%d, %v", channel, uploader, err)
			}
			if used, err := s.UploaderContentUsageExcept(ctx, 0, global.SHA256, global.Uploader, global.Folder, global.Name); err != nil || used != 20 {
				t.Fatalf("global remaining usage = %d, %v", used, err)
			}
			if err := s.MoveFile(ctx, 0, global.Folder, global.Name, channelID, "", global.Name); err != nil {
				t.Fatalf("global to channel move: %v", err)
			}
			if used, err := s.UploaderFileUsage(ctx, global.Uploader); err != nil || used != 40 {
				t.Fatalf("cross-scope quota = %d, %v", used, err)
			}
			if err := s.MoveFile(ctx, channelID, "", global.Name, 0, "copies", global.Name); !errors.Is(err, ErrFileExists) {
				t.Fatalf("global destination collision = %v", err)
			}
			if err := s.MoveFile(ctx, channelID, "", global.Name, 0, "", "moved.noxac"); err != nil {
				t.Fatalf("channel to global move: %v", err)
			}
			if rec, err := s.GetFile(ctx, 0, "", "moved.noxac"); err != nil || rec.ChannelID != 0 || !rec.Encrypted {
				t.Fatalf("moved global attachment = %+v, %v", rec, err)
			}

			const missingChannel = int64(9223372036854775807)
			invalid := global
			invalid.ChannelID = missingChannel
			for _, err := range []error{
				s.AddFile(ctx, invalid),
				s.MoveFile(ctx, 0, "", "moved.noxac", missingChannel, "", "missing.noxac"),
			} {
				var foreignKey *pq.Error
				if !errors.As(err, &foreignKey) || foreignKey.Code != "23503" {
					t.Fatalf("missing real-channel foreign key was not enforced: %v", err)
				}
			}
			if _, err := s.DB().ExecContext(ctx, `DELETE FROM channels WHERE id = $1`, channelID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetFile(ctx, channelID, "", "existing.txt"); !errors.Is(err, ErrFileNotFound) {
				t.Fatalf("real-channel file survived channel deletion: %v", err)
			}
			if _, err := s.GetFile(ctx, 0, "", "moved.noxac"); err != nil {
				t.Fatalf("channel deletion removed global attachment: %v", err)
			}
			if err := s.DeleteFile(ctx, 0, "", "moved.noxac"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetFile(ctx, 0, "", "moved.noxac"); !errors.Is(err, ErrFileNotFound) {
				t.Fatalf("deleted global attachment remains: %v", err)
			}
			if err := s.Migrate(); err != nil {
				t.Fatalf("repeat migration: %v", err)
			}
			if _, err := s.GetFile(ctx, 0, "copies", global.Name); err != nil {
				t.Fatalf("repeat migration removed global attachment: %v", err)
			}
		})
	}
}

// TestFilesDB covers the folder-aware file metadata: add/list per folder,
// rename/move, version listing, dedup lookup, and delete.
func TestFilesDB(t *testing.T) {
	s := testDBStore(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())

	var channelID int64
	if err := s.DB().QueryRowContext(ctx,
		`INSERT INTO channels (name, channel_type, created_at) VALUES ($1, 2, NOW()) RETURNING id`,
		"w7_files_"+suffix).Scan(&channelID); err != nil {
		t.Fatalf("seeding channel: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.DB().ExecContext(ctx, `DELETE FROM channels WHERE id = $1`, channelID)
	})

	add := func(folder, name, sha string, size int64) {
		t.Helper()
		if err := s.AddFile(ctx, FileRecord{ChannelID: channelID, Folder: folder, Name: name, Size: size, SHA256: sha, Uploader: "u"}); err != nil {
			t.Fatalf("AddFile %s/%s: %v", folder, name, err)
		}
	}

	add("", "a.txt", "sha-a", 10)
	add("docs", "a.txt", "sha-a2", 20)
	add("docs/2024", "b.txt", "sha-b", 30)

	// Same name in different folders coexists; listing is folder-scoped.
	root, err := s.ListFiles(ctx, channelID, "")
	if err != nil || len(root) != 1 || root[0].Size != 10 {
		t.Fatalf("root files = %+v, err=%v", root, err)
	}
	docs, err := s.ListFiles(ctx, channelID, "docs")
	if err != nil || len(docs) != 1 || docs[0].Folder != "docs" {
		t.Fatalf("docs files = %+v, err=%v", docs, err)
	}

	// Overwrite in one folder leaves the other folder's row alone.
	add("", "a.txt", "sha-a-new", 11)
	docs, _ = s.ListFiles(ctx, channelID, "docs")
	if len(docs) != 1 || docs[0].Size != 20 {
		t.Fatalf("docs files after root overwrite = %+v", docs)
	}

	// Folders are derived from rows.
	folders, err := s.ListFileFolders(ctx, channelID)
	if err != nil || len(folders) != 2 {
		t.Fatalf("folders = %v, err=%v", folders, err)
	}

	// Rename + move.
	if err := s.RenameFile(ctx, channelID, "docs/2024", "b.txt", "docs", "b-renamed.txt"); err != nil {
		t.Fatalf("RenameFile: %v", err)
	}
	rec, err := s.GetFile(ctx, channelID, "docs", "b-renamed.txt")
	if err != nil || rec.Size != 30 {
		t.Fatalf("moved record = %+v, err=%v", rec, err)
	}
	if _, err := s.GetFile(ctx, channelID, "docs/2024", "b.txt"); err != ErrFileNotFound {
		t.Fatalf("old record still present: %v", err)
	}

	// Versions.
	add("", "v.txt", "sha-v3", 3)
	add("", "v.txt.v1", "sha-v2", 2)
	add("", "v.txt.v2", "sha-v1", 1)
	versions, err := s.ListFileVersions(ctx, channelID, "", "v.txt")
	if err != nil || len(versions) != 2 || versions[0].Name != "v.txt.v1" {
		t.Fatalf("versions = %+v, err=%v", versions, err)
	}

	// Dedup lookup finds the identical blob elsewhere in the channel.
	dup, err := s.FindFileBySHA(ctx, channelID, "sha-a-new", "docs", "other.txt")
	if err != nil || dup == nil || dup.Name != "a.txt" {
		t.Fatalf("FindFileBySHA = %+v, err=%v", dup, err)
	}
	if dup, _ := s.FindFileBySHA(ctx, channelID, "sha-nope", "", "x"); dup != nil {
		t.Fatalf("unexpected dedup hit: %+v", dup)
	}

	// Delete.
	if err := s.DeleteFile(ctx, channelID, "", "a.txt"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if _, err := s.GetFile(ctx, channelID, "", "a.txt"); err != ErrFileNotFound {
		t.Fatalf("record after delete: %v", err)
	}
	if err := s.DeleteFile(ctx, channelID, "", "a.txt"); err != ErrFileNotFound {
		t.Fatalf("second delete = %v, want ErrFileNotFound", err)
	}
}

// TestFileQuotaUsageDB covers the two quota axes (265/266). Dedup hard-links
// identical blobs inside a channel, so the channel axis must charge a content
// hash once however many rows point at it; across channels the blob really is
// stored twice, so the uploader axis charges it twice.
func TestFileQuotaUsageDB(t *testing.T) {
	s := testDBStore(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())

	newChannel := func(name string) int64 {
		t.Helper()
		var id int64
		if err := s.DB().QueryRowContext(ctx,
			`INSERT INTO channels (name, channel_type, created_at) VALUES ($1, 2, NOW()) RETURNING id`,
			name).Scan(&id); err != nil {
			t.Fatalf("seeding channel: %v", err)
		}
		t.Cleanup(func() {
			_, _ = s.DB().ExecContext(ctx, `DELETE FROM channels WHERE id = $1`, id)
		})
		return id
	}
	chA := newChannel("w7_quota_a_" + suffix)
	chB := newChannel("w7_quota_b_" + suffix)

	uploader := "quota-uid-" + suffix
	add := func(channelID int64, folder, name, sha string, size int64, who string) {
		t.Helper()
		if err := s.AddFile(ctx, FileRecord{
			ChannelID: channelID, Folder: folder, Name: name, Size: size, SHA256: sha, Uploader: who,
		}); err != nil {
			t.Fatalf("AddFile %s: %v", name, err)
		}
	}

	// Two rows, one blob: the second is a hard link and costs no disk.
	add(chA, "", "one.bin", "sha-dup-"+suffix, 1000, uploader)
	add(chA, "copies", "two.bin", "sha-dup-"+suffix, 1000, uploader)
	add(chA, "", "other.bin", "sha-other-"+suffix, 500, "someone-else")
	// The same content in another channel is a separate blob.
	add(chB, "", "three.bin", "sha-dup-"+suffix, 1000, uploader)

	used, err := s.ChannelFileUsage(ctx, chA)
	if err != nil {
		t.Fatalf("ChannelFileUsage: %v", err)
	}
	if used != 1500 {
		t.Errorf("channel usage = %d, want 1500 (deduped copy charged twice?)", used)
	}

	mine, err := s.UploaderFileUsage(ctx, uploader)
	if err != nil {
		t.Fatalf("UploaderFileUsage: %v", err)
	}
	if mine != 2000 {
		t.Errorf("uploader usage = %d, want 2000 (one blob per channel)", mine)
	}

	none, err := s.UploaderFileUsage(ctx, "")
	if err != nil || none != 0 {
		t.Errorf("empty uploader usage = %d, err=%v, want 0", none, err)
	}
	for _, tc := range []struct {
		name, hash, who   string
		channel, uploader int64
	}{
		{"deduplicated", "sha-dup-" + suffix, uploader, 1000, 1000},
		{"another_uploader", "sha-other-" + suffix, uploader, 500, 0},
		{"missing_hash", "absent", uploader, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel, personal, err := s.FileContentUsage(ctx, chA, tc.hash, tc.who)
			if err != nil || channel != tc.channel || personal != tc.uploader {
				t.Fatalf("content usage = %d/%d, %v; want %d/%d", channel, personal, err, tc.channel, tc.uploader)
			}
		})
	}
	for _, tc := range []struct {
		name               string
		channel            int64
		folder, file, hash string
		want               int64
	}{
		{"source_copy_remains", chA, "", "one.bin", "sha-dup-" + suffix, 1000},
		{"last_reference", chB, "", "three.bin", "sha-dup-" + suffix, 0},
		{"other_uploader", chA, "", "missing", "sha-other-" + suffix, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.UploaderContentUsageExcept(ctx, tc.channel, tc.hash, uploader, tc.folder, tc.file)
			if err != nil || got != tc.want {
				t.Fatalf("remaining content = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}
