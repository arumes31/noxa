// orphan_sweep.go reconciles stored blobs with the files table (61). It runs
// as a bounded retention task: every tick processes channels until its
// context deadline, remembers where it stopped, and starts a new pass once
// per orphanSweepInterval.
//
// What it removes, always only from canonical per-channel directories:
//   - single-shot ".part-<16 hex>" leftovers older than legacyPartialTTL;
//   - resumable ".part-r<32 hex>" partials untouched for resumablePartialTTL
//     that no live transfer is writing;
//   - blobs with no files row, once older than the configured grace.
//
// Every path the files table references is left alone, so a deduplicated
// blob (hard links, 275) is only unlinked under names no row uses. Each
// removal re-checks the row while holding fileOpsMu, the lock that upload
// finalization, rotation, rename, move and delete take, so the sweep cannot
// race a file being committed under a name it is about to remove.
package filetransfer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"noxa/internal/store"
)

// orphanSweepInterval is how often a full pass starts.
const orphanSweepInterval = 6 * time.Hour

// DefaultOrphanGrace is how old an unreferenced blob must be before the sweep
// removes it. Uploads commit within connTimeout, so a day leaves ample room.
const DefaultOrphanGrace = 24 * time.Hour

// MinOrphanGrace keeps a misconfiguration from racing live uploads.
const MinOrphanGrace = time.Hour

// OrphanPolicy configures the sweep (file_orphan_* keys).
type OrphanPolicy struct {
	// Disabled turns the whole sweep off, partial cleanup included.
	Disabled bool
	// DryRun logs and counts what would be removed without removing it.
	DryRun bool
	// Grace is the minimum age of an unreferenced blob before removal.
	Grace time.Duration
	// DeleteMissingRows removes files rows whose blob is gone. Off by default:
	// the rows are reported, because a missing blob can be an unmounted volume.
	DeleteMissingRows bool
}

// OrphanSweepResult counts one pass, or one tick of a pass.
type OrphanSweepResult struct {
	StalePartials  int
	OrphanBlobs    int
	OrphanBytes    int64
	MissingBlobs   int
	RowsRemoved    int
	ChannelsDone   int
	PassCompleted  bool
	DryRun         bool
	RemovalFailure int
}

type orphanSweepState struct {
	next     time.Time // when the next pass may start
	pending  []int64   // channels left in the current pass
	running  bool
	total    OrphanSweepResult
	lastPass OrphanSweepResult
}

// SetOrphanPolicy installs the sweep configuration. Call before Start.
func (s *Server) SetOrphanPolicy(policy OrphanPolicy) {
	if policy.Grace < MinOrphanGrace {
		policy.Grace = DefaultOrphanGrace
	}
	s.mu.Lock()
	s.orphanPolicy = policy
	s.mu.Unlock()
}

// PruneOrphanedFiles is the retention task. It returns quickly when no pass
// is due, and otherwise processes channels until ctx ends.
func (s *Server) PruneOrphanedFiles(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	policy := s.orphanPolicy
	if policy.Grace < MinOrphanGrace {
		policy.Grace = DefaultOrphanGrace
	}
	if policy.Disabled {
		s.mu.Unlock()
		return nil
	}
	if !s.sweep.running {
		if now.Before(s.sweep.next) {
			s.mu.Unlock()
			return nil
		}
		s.sweep.running = true
		s.sweep.total = OrphanSweepResult{DryRun: policy.DryRun}
		s.sweep.pending = nil
		s.mu.Unlock()
		channels, err := s.channelDirectories()
		if err != nil {
			s.mu.Lock()
			s.sweep.running = false
			s.sweep.next = now.Add(orphanSweepInterval)
			s.mu.Unlock()
			return err
		}
		s.mu.Lock()
		s.sweep.pending = channels
	}
	s.mu.Unlock()

	var errs []error
	for ctx.Err() == nil {
		s.mu.Lock()
		if len(s.sweep.pending) == 0 {
			s.sweep.running = false
			s.sweep.next = now.Add(orphanSweepInterval)
			s.sweep.total.PassCompleted = true
			s.sweep.lastPass = s.sweep.total
			result := s.sweep.total
			s.mu.Unlock()
			s.pruneIdlePartials("", now)
			s.reportOrphanPass(result)
			return errors.Join(errs...)
		}
		channelID := s.sweep.pending[0]
		s.mu.Unlock()

		result, err := s.sweepChannel(ctx, channelID, now, policy)
		if err != nil && ctx.Err() != nil {
			// The channel restarts on the next tick. What this tick already
			// removed will not be found again, so count it now; a dry run
			// removed nothing and would count the same files twice.
			if !policy.DryRun {
				s.mu.Lock()
				s.sweep.total.add(result)
				s.mu.Unlock()
			}
			break
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("channel %d: %w", channelID, err))
		}
		s.mu.Lock()
		if len(s.sweep.pending) > 0 && s.sweep.pending[0] == channelID {
			s.sweep.pending = s.sweep.pending[1:]
		}
		s.sweep.total.add(result)
		s.sweep.total.ChannelsDone++
		s.mu.Unlock()
	}
	return errors.Join(errs...)
}

// LastOrphanSweep returns the result of the most recent completed pass.
func (s *Server) LastOrphanSweep() OrphanSweepResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweep.lastPass
}

func (r *OrphanSweepResult) add(o OrphanSweepResult) {
	r.StalePartials += o.StalePartials
	r.OrphanBlobs += o.OrphanBlobs
	r.OrphanBytes += o.OrphanBytes
	r.MissingBlobs += o.MissingBlobs
	r.RowsRemoved += o.RowsRemoved
	r.RemovalFailure += o.RemovalFailure
}

func (s *Server) reportOrphanPass(r OrphanSweepResult) {
	if s.OnOrphanSweep != nil {
		s.OnOrphanSweep(r)
	}
	if r.StalePartials == 0 && r.OrphanBlobs == 0 && r.MissingBlobs == 0 && r.RemovalFailure == 0 {
		return
	}
	s.logger.Info("file storage reconciled",
		zap.Bool("dry_run", r.DryRun),
		zap.Int("stale_partials", r.StalePartials),
		zap.Int("orphan_blobs", r.OrphanBlobs),
		zap.Int64("orphan_bytes", r.OrphanBytes),
		zap.Int("rows_missing_blob", r.MissingBlobs),
		zap.Int("rows_removed", r.RowsRemoved),
		zap.Int("removal_failures", r.RemovalFailure),
		zap.Int("channels", r.ChannelsDone),
	)
}

// channelDirectories lists canonical positive-decimal channel directories,
// the only ones the sweep owns (asset namespaces are left alone).
func (s *Server) channelDirectories() ([]int64, error) {
	root, err := s.openBlobRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id, err := strconv.ParseInt(entry.Name(), 10, 64)
		if err != nil || id < 0 || strconv.FormatInt(id, 10) != entry.Name() {
			continue
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// sweepChannel reconciles one channel directory.
func (s *Server) sweepChannel(ctx context.Context, channelID int64, now time.Time, policy OrphanPolicy) (OrphanSweepResult, error) {
	result := OrphanSweepResult{DryRun: policy.DryRun}
	if s.channelDeleted(channelID) {
		return result, nil // channel cleanup owns the directory now
	}
	rows, err := s.channelRows(ctx, channelID)
	if err != nil {
		return result, err
	}
	referenced := make(map[string]store.FileRecord, len(rows))
	for _, rec := range rows {
		referenced[filepath.Clean(blobPath(channelID, rec.Folder, rec.Name))] = rec
	}

	root, err := s.openBlobRoot()
	if err != nil {
		return result, err
	}
	defer func() { _ = root.Close() }()
	channelDir := strconv.FormatInt(channelID, 10)
	seen := make(map[string]bool, len(referenced))

	walkErr := fs.WalkDir(root.FS(), filepath.ToSlash(channelDir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil // symlinks and devices are never ours to follow or remove
		}
		rel := filepath.Clean(filepath.FromSlash(p))
		if _, ok := referenced[rel]; ok {
			seen[rel] = true
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		age := now.Sub(info.ModTime())
		base := filepath.Base(rel)
		switch {
		case isResumablePartialName(base):
			if age < resumablePartialTTL || s.partialInUse(rel) {
				return nil
			}
			if s.removeUnreferenced(ctx, channelID, rel, policy.DryRun, &result) {
				result.StalePartials++
				if !policy.DryRun {
					s.forgetPartial(rel)
				}
			}
		case isLegacyPartialName(base):
			if age < legacyPartialTTL {
				return nil
			}
			if s.removeUnreferenced(ctx, channelID, rel, policy.DryRun, &result) {
				result.StalePartials++
			}
		default:
			if age < policy.Grace {
				return nil
			}
			if s.removeUnreferenced(ctx, channelID, rel, policy.DryRun, &result) {
				result.OrphanBlobs++
				result.OrphanBytes += info.Size()
			}
		}
		return nil
	})
	if walkErr != nil {
		return result, walkErr
	}

	for rel, rec := range referenced {
		if seen[rel] {
			continue
		}
		if _, err := root.Lstat(rel); err == nil || !errors.Is(err, fs.ErrNotExist) {
			continue // present (created after the walk) or unknown: leave it
		}
		result.MissingBlobs++
		s.logger.Warn("file row has no stored blob",
			zap.Int64("channel_id", channelID), zap.String("folder", rec.Folder), zap.String("name", rec.Name))
		if policy.DeleteMissingRows && !policy.DryRun && s.deleteMissingRow(ctx, channelID, rec, rel) {
			result.RowsRemoved++
		}
	}
	return result, nil
}

// channelRows lists every files row of a channel, versions included.
func (s *Server) channelRows(ctx context.Context, channelID int64) ([]store.FileRecord, error) {
	folders, err := s.store.ListFileFolders(ctx, channelID)
	if err != nil {
		return nil, err
	}
	var out []store.FileRecord
	for _, folder := range append([]string{""}, folders...) {
		rows, err := s.store.ListFiles(ctx, channelID, folder)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// removeUnreferenced removes rel unless a files row now references it. It
// reports whether the path was (or, in a dry run, would have been) removed.
func (s *Server) removeUnreferenced(ctx context.Context, channelID int64, rel string, dryRun bool, result *OrphanSweepResult) bool {
	folder, name, ok := splitBlobPath(channelID, rel)
	if !ok {
		return false
	}
	s.fileOpsMu.Lock()
	defer s.fileOpsMu.Unlock()
	if s.channelDeleted(channelID) {
		return false
	}
	if _, err := s.store.GetFile(ctx, channelID, folder, name); err == nil || !errors.Is(err, store.ErrFileNotFound) {
		return false // committed meanwhile, or the store is unsure
	}
	if dryRun {
		s.logger.Info("orphan sweep would remove", zap.Int64("channel_id", channelID), zap.String("path", filepath.ToSlash(rel)))
		return true
	}
	root, err := s.openBlobRoot()
	if err != nil {
		result.RemovalFailure++
		return false
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(rel)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
		result.RemovalFailure++
		s.logger.Warn("orphan sweep could not remove a file", zap.String("path", filepath.ToSlash(rel)), zap.Error(err))
		return false
	}
	return true
}

// deleteMissingRow removes a row whose blob is still missing under the lock.
func (s *Server) deleteMissingRow(ctx context.Context, channelID int64, rec store.FileRecord, rel string) bool {
	s.fileOpsMu.Lock()
	defer s.fileOpsMu.Unlock()
	root, err := s.openBlobRoot()
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat(rel); !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err := s.store.DeleteFile(ctx, channelID, rec.Folder, rec.Name); err != nil {
		s.logger.Warn("orphan sweep could not remove a file row", zap.Int64("channel_id", channelID), zap.String("name", rec.Name), zap.Error(err))
		return false
	}
	return true
}

// splitBlobPath turns "<channel>/<folder...>/<name>" back into the folder and
// name a files row would carry. Paths outside the channel are refused.
func splitBlobPath(channelID int64, rel string) (string, string, bool) {
	prefix := strconv.FormatInt(channelID, 10) + string(filepath.Separator)
	if !strings.HasPrefix(rel, prefix) {
		return "", "", false
	}
	inner := filepath.ToSlash(strings.TrimPrefix(rel, prefix))
	if inner == "" || strings.HasPrefix(inner, "../") || strings.Contains(inner, "/../") {
		return "", "", false
	}
	folder, name := "", inner
	if i := strings.LastIndex(inner, "/"); i >= 0 {
		folder, name = inner[:i], inner[i+1:]
	}
	if name == "" {
		return "", "", false
	}
	return folder, name, true
}
