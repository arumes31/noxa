package main

import (
	"errors"
	"io"
	"noxa/internal/netproto"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const fileGrantLifetime = 30 * time.Minute
const maxFileGrants = 256

var errNativeFileSelection = errors.New("select this file or folder in the native picker first")

type fileGrant struct {
	path     string
	parent   os.FileInfo
	file     os.FileInfo
	expires  time.Time
	selected uint64
}
type fileGrantStore struct {
	mu       sync.Mutex
	entries  map[string]fileGrant
	sequence uint64
}

func fileGrantKey(kind, path string) string {
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return kind + ":" + path
}
func canonicalFilePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if !netproto.ValidFileLeaf(filepath.Base(abs)) {
		return "", errors.New("invalid filename")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}
func (a *App) grantNativePath(kind, path string) (string, error) {
	var err error
	if kind == "folder" {
		path, err = filepath.Abs(path)
		if err == nil {
			path, err = filepath.EvalSymlinks(path)
		}
	} else {
		path, err = canonicalFilePath(path)
	}
	if err != nil {
		return "", err
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || !parent.IsDir() {
		return "", errNativeFileSelection
	}
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if info != nil && (info.Mode()&os.ModeSymlink != 0 || kind != "folder" && !info.Mode().IsRegular() || kind == "folder" && !info.IsDir()) {
		return "", errNativeFileSelection
	}
	// Windows loads file IDs lazily. Capture them while the selected paths
	// still refer to the files observed by the native dialog.
	if !os.SameFile(parent, parent) || info != nil && !os.SameFile(info, info) {
		return "", errNativeFileSelection
	}
	if (kind == "upload" || kind == "folder") && info == nil {
		return "", errNativeFileSelection
	}
	a.fileGrants.mu.Lock()
	defer a.fileGrants.mu.Unlock()
	if a.fileGrants.entries == nil {
		a.fileGrants.entries = make(map[string]fileGrant)
	}
	now := time.Now()
	for key, g := range a.fileGrants.entries {
		if !g.expires.IsZero() && now.After(g.expires) {
			delete(a.fileGrants.entries, key)
		}
	}
	key := fileGrantKey(kind, path)
	if _, ok := a.fileGrants.entries[key]; !ok && len(a.fileGrants.entries) >= maxFileGrants {
		// Grants are capabilities retained for retries, not a permanent quota.
		// Active transfers already own their opened file/root handles.
		oldestKey := ""
		var oldest uint64
		for candidate, grant := range a.fileGrants.entries {
			if oldestKey == "" || grant.selected < oldest {
				oldestKey, oldest = candidate, grant.selected
			}
		}
		delete(a.fileGrants.entries, oldestKey)
	}
	expiry := now.Add(fileGrantLifetime)
	// The retry UI retains a native save selection for the application session,
	// including interrupted transfers that last longer than the picker timeout.
	if kind == "download" {
		expiry = time.Time{}
	}
	// Clock resolution must not decide which capability is evicted: Windows
	// can report identical timestamps for several consecutive selections.
	a.fileGrants.sequence++
	a.fileGrants.entries[key] = fileGrant{path: path, parent: parent, file: info, expires: expiry, selected: a.fileGrants.sequence}
	return path, nil
}
func (a *App) nativeGrant(kind, path string) (fileGrant, bool) {
	a.fileGrants.mu.Lock()
	defer a.fileGrants.mu.Unlock()
	g, ok := a.fileGrants.entries[fileGrantKey(kind, path)]
	return g, ok && (g.expires.IsZero() || time.Now().Before(g.expires))
}
func openGrantParent(g fileGrant) (*os.Root, error) {
	root, err := os.OpenRoot(filepath.Dir(g.path))
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil || !os.SameFile(info, g.parent) {
		_ = root.Close()
		return nil, errNativeFileSelection
	}
	return root, nil
}
func (a *App) openUploadSelection(path string) (*os.File, error) {
	canonical, err := canonicalFilePath(path)
	if err != nil {
		return nil, errNativeFileSelection
	}
	grant, ok := a.nativeGrant("upload", canonical)
	if !ok {
		return nil, errNativeFileSelection
	}
	root, err := openGrantParent(grant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	leaf := filepath.Base(canonical)
	before, err := root.Lstat(leaf)
	if err != nil || !before.Mode().IsRegular() || !os.SameFile(before, grant.file) {
		return nil, errNativeFileSelection
	}
	src, err := root.Open(leaf)
	if err != nil {
		return nil, err
	}
	opened, err := src.Stat()
	if err != nil || !os.SameFile(opened, grant.file) {
		_ = src.Close()
		return nil, errNativeFileSelection
	}
	return src, nil
}
func (a *App) validateDownloadFolder(current, incoming string) error {
	if incoming == "" || incoming == current {
		return nil
	}
	path, err := filepath.Abs(incoming)
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil {
		return errNativeFileSelection
	}
	grant, ok := a.nativeGrant("folder", path)
	if !ok {
		return errNativeFileSelection
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || !os.SameFile(info, grant.file) {
		return errNativeFileSelection
	}
	return nil
}

type downloadDestination struct {
	root     *os.Root
	leaf     string
	existing os.FileInfo
}

func (d *downloadDestination) Close() { _ = d.root.Close() }
func (a *App) openDownloadSelection(path string) (*downloadDestination, error) {
	canonical, err := canonicalFilePath(path)
	if err != nil {
		return nil, errNativeFileSelection
	}
	grant, ok := a.nativeGrant("download", canonical)
	if !ok {
		a.settingsMu.Lock()
		dir := a.settings.DownloadFolder
		a.settingsMu.Unlock()
		if dir == "" {
			return nil, errNativeFileSelection
		}
		dir, err = filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, errNativeFileSelection
		}
		dir, err = filepath.Abs(dir)
		if err != nil || fileGrantKey("", dir) != fileGrantKey("", filepath.Dir(canonical)) {
			return nil, errNativeFileSelection
		}
		parent, err := os.Stat(dir)
		if err != nil || !parent.IsDir() {
			return nil, errNativeFileSelection
		}
		grant = fileGrant{path: canonical, parent: parent}
	}
	root, err := openGrantParent(grant)
	if err != nil {
		return nil, err
	}
	dest := &downloadDestination{root: root, leaf: filepath.Base(canonical), existing: grant.file}
	if err = dest.checkDestination(); err != nil {
		dest.Close()
		return nil, err
	}
	return dest, nil
}
func (d *downloadDestination) checkDestination() error {
	info, err := d.root.Lstat(d.leaf)
	if errors.Is(err, os.ErrNotExist) {
		if d.existing == nil {
			return nil
		}
		return errors.New("selected destination changed; select it again")
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || d.existing == nil || !os.SameFile(info, d.existing) {
		return errors.New("destination already exists or changed; choose another filename")
	}
	return nil
}

// Replaceable in tests for filesystems without hardlink support.
var linkDownloadPart = func(root *os.Root, part, dest string) error { return root.Link(part, dest) }

func (d *downloadDestination) finalize() error {
	if err := d.checkDestination(); err != nil {
		return err
	}
	part := d.leaf + partSuffix
	if d.existing != nil {
		return d.root.Rename(part, d.leaf)
	}
	// Link is atomic and cannot replace a file that appeared since the check.
	if err := linkDownloadPart(d.root, part, d.leaf); err != nil {
		// FAT/exFAT cannot hardlink. Exclusive creation still prevents replacing an
		// existing destination; retain the verified partial if publication fails.
		src, err := d.root.Open(part)
		if err != nil {
			return err
		}
		defer func() { _ = src.Close() }()
		out, err := d.root.OpenFile(d.leaf, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, src)
		if copyErr == nil {
			copyErr = out.Sync()
		}
		info, statErr := out.Stat()
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			if current, err := d.root.Lstat(d.leaf); statErr == nil && err == nil && os.SameFile(current, info) {
				_ = d.root.Remove(d.leaf)
			}
		}
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return d.root.Remove(part)
}
