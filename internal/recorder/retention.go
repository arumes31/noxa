package recorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var captureManifestName = regexp.MustCompile(`^channel-([1-9][0-9]*)-[a-f0-9]{32}\.json$`)
var captureMediaName = regexp.MustCompile(`^channel-[1-9][0-9]*-[0-9]{8}-[0-9]{6}\.[0-9]{9}-[a-f0-9]{24}\.(webm|mp4)$`)

const maxPruneFiles = 128
const maxManifestBytes = 2 << 20

type recordingPruneScan struct {
	root    *os.Root
	dir     *os.File
	path    string
	pending string
}

func (c *ChannelRecorder) closePruneScan() {
	c.pruneMu.Lock()
	defer c.pruneMu.Unlock()
	c.resetPruneScan()
}
func (c *ChannelRecorder) resetPruneScan() {
	if c.pruneScan != nil {
		_ = c.pruneScan.dir.Close()
		_ = c.pruneScan.root.Close()
		c.pruneScan = nil
	}
}

// PruneExpiredRecordings expires finished recording sets 30 days after completion.
// Valid abandoned manifests use their last write time, so a crash cannot retain
// an otherwise recognizable set indefinitely. Unknown/corrupt files are never
// interpreted as deletion instructions. Each call scans at most 128 directory
// entries and unlinks at most 128 files; its cursor survives between batches.
func (c *ChannelRecorder) PruneExpiredRecordings(ctx context.Context, now time.Time) error {
	c.pruneMu.Lock()
	defer c.pruneMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.pruneScan == nil {
		path, err := filepath.Abs(c.processes.cfg.Dir)
		if err != nil {
			return err
		}
		if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		root, err := openRecordingRoot(path)
		if err != nil {
			return err
		}
		dir, err := root.Open(".")
		if err != nil {
			_ = root.Close()
			return err
		}
		c.pruneScan = &recordingPruneScan{root: root, dir: dir, path: path}
	}
	scan := c.pruneScan
	if err := verifyRecordingRoot(scan.path, scan.root); err != nil {
		c.resetPruneScan()
		return err
	}
	cutoff := now.UTC().Add(-30 * 24 * time.Hour)
	budget := maxPruneFiles
	var result error
	for range 128 {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		name := scan.pending
		if name == "" {
			entries, err := scan.dir.ReadDir(1)
			if errors.Is(err, io.EOF) {
				c.resetPruneScan()
				break
			}
			if err != nil {
				c.resetPruneScan()
				return errors.Join(result, err)
			}
			// Entries may disappear while this directory cursor is being read.
			if len(entries) == 0 {
				continue
			}
			name = entries[0].Name()
		}
		scan.pending = ""
		match := captureManifestName.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		channelID, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			continue
		}
		// Start reservations and cleanup-owned captures remain in sessions until
		// all child processes and collaborators are reaped. Hold that same lock
		// through removal so a new session cannot appear during this check.
		c.mu.Lock()
		if c.sessions[channelID] != nil || c.closed {
			c.mu.Unlock()
			continue
		}
		complete, err := c.pruneCapture(ctx, scan, name, channelID, cutoff, &budget)
		c.mu.Unlock()
		if err != nil {
			result = errors.Join(result, fmt.Errorf("recording retention: %w", err))
		}
		if !complete && err == nil {
			scan.pending = name
		}
		if budget == 0 {
			break
		}
	}
	return result
}

func (c *ChannelRecorder) pruneCapture(ctx context.Context, scan *recordingPruneScan, name string, channelID int64, cutoff time.Time, budget *int) (bool, error) {
	info, err := scan.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	if !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
		return true, nil
	}
	if info.Size() > maxManifestBytes {
		return true, errors.New("recording manifest exceeds size limit; retained")
	}
	file, err := scan.root.Open(name)
	if err != nil {
		return true, err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return true, errors.New("recording manifest changed during inspection")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	err = errors.Join(err, file.Close())
	if err != nil {
		return true, err
	}
	var manifest captureManifest
	if len(raw) > maxManifestBytes || json.Unmarshal(raw, &manifest) != nil || manifest.Version != 1 || manifest.ChannelID != channelID || manifest.StartedAt.IsZero() || !manifest.StartedAt.Before(cutoff) || len(manifest.Tracks) > 1024 {
		return true, errors.New("invalid recording manifest; retained")
	}
	if manifest.EndedAt != nil && !manifest.EndedAt.Before(cutoff) {
		return true, nil
	}
	names := make([]string, 0, len(manifest.Tracks)*2+1)
	seen := make(map[string]bool)
	for _, track := range manifest.Tracks {
		if track == nil || !captureMediaName.MatchString(track.File) {
			return true, errors.New("invalid recording track name; retained")
		}
		for _, leaf := range []string{track.File, strings.TrimSuffix(track.File, filepath.Ext(track.File)) + ".sdp"} {
			if !seen[leaf] {
				names = append(names, leaf)
				seen[leaf] = true
			}
		}
	}
	// Validate the complete set before deleting anything. Restrict names as well
	// as using os.Root: a forged manifest must not name arbitrary local files.
	names = append(names, name+".tmp", name)
	for _, leaf := range names {
		entry, err := scan.root.Lstat(leaf)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return true, err
		}
		if !entry.Mode().IsRegular() {
			return true, errors.New("recording set contains a non-regular file; retained")
		}
		if !entry.ModTime().Before(cutoff) {
			return true, nil
		}
	}
	// Also defend against a corrupt manifest naming a currently owned process.
	c.processes.mu.Lock()
	defer c.processes.mu.Unlock()
	if len(c.processes.starting) != 0 {
		return true, nil
	}
	for _, session := range c.processes.sessions {
		if seen[filepath.Base(session.FilePath)] {
			return true, nil
		}
	}
	for _, leaf := range names {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if *budget == 0 {
			return false, nil
		}
		if err := verifyRecordingRoot(scan.path, scan.root); err != nil {
			return false, err
		}
		if err := scan.root.Remove(leaf); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return false, err
		}
		*budget--
	}
	return true, nil
}
