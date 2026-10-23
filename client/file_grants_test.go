package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeFileGrants(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(path, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &App{}
	if f, err := a.openUploadSelection(path); err == nil {
		_ = f.Close()
		t.Fatal("unselected upload accepted")
	}
	if d, err := a.openDownloadSelection(path); err == nil {
		d.Close()
		t.Fatal("unselected download accepted")
	}
	if err := a.validateDownloadFolder("", dir); err == nil {
		t.Fatal("renderer changed folder")
	}
	if _, err := a.grantNativePath("folder", dir); err != nil {
		t.Fatal(err)
	}
	if err := a.validateDownloadFolder("", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := a.grantNativePath("upload", path); err != nil {
		t.Fatal(err)
	}
	f, err := a.openUploadSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	a.fileGrants.mu.Lock()
	for k, g := range a.fileGrants.entries {
		g.expires = time.Now().Add(-time.Second)
		a.fileGrants.entries[k] = g
	}
	a.fileGrants.mu.Unlock()
	if f, err := a.openUploadSelection(path); err == nil {
		_ = f.Close()
		t.Fatal("expired grant accepted")
	}
}
func TestConfiguredDownloadNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	a := &App{settings: Settings{DownloadFolder: dir}}
	d, err := a.openDownloadSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := os.WriteFile(path+partSuffix, []byte("incoming"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.finalize(); err == nil {
		t.Fatal("overwrote file created during transfer")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "existing" {
		t.Fatal("existing file changed")
	}
	if a.DownloadPath("file.txt") != "" {
		t.Fatal("collision did not request picker")
	}
	if d, err := a.openDownloadSelection(filepath.Join(t.TempDir(), "escape")); err == nil {
		d.Close()
		t.Fatal("escaped configured folder")
	}
}
func TestNativeGrantsRejectReplacedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	a := &App{}
	if err := os.WriteFile(path, []byte("selected"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.grantNativePath("upload", path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("substituted"), 0600); err != nil {
		t.Fatal(err)
	}
	if f, err := a.openUploadSelection(path); err == nil {
		_ = f.Close()
		t.Fatal("substituted upload accepted")
	}
}
func TestPartialDownloadRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path+partSuffix); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	d, err := fixtureDestination(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if f, _, _, err := resumeInRoot(d); err == nil {
		_ = f.Close()
		t.Fatal("symlink partial accepted")
	}
}

func TestSaveSettingsCannotForgeDownloadFolder(t *testing.T) {
	a := &App{settings: DefaultSettings(), settingsPath: filepath.Join(t.TempDir(), "settings.json")}
	incoming := DefaultSettings()
	incoming.DownloadFolder = t.TempDir()
	if got := a.SaveSettings(incoming); got != errNativeFileSelection.Error() {
		t.Fatalf("forged folder returned %q", got)
	}
	if a.settings.DownloadFolder != "" {
		t.Fatal("forged settings persisted")
	}
}
func TestNativeFolderGrantAcceptsVolumeRoot(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	a := &App{}
	if _, err := a.grantNativePath("folder", root); err != nil {
		t.Fatal(err)
	}
	if err := a.validateDownloadFolder("", root); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadPublicationWithoutHardlinks(t *testing.T) {
	previous := linkDownloadPart
	linkDownloadPart = func(*os.Root, string, string) error { return os.ErrPermission }
	t.Cleanup(func() { linkDownloadPart = previous })
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path+partSuffix, []byte("verified"), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := fixtureDestination(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.finalize(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "verified" {
		t.Fatalf("publication: %q %v", got, err)
	}
}

func TestNativeSelectionsEvictOldGrantsWithoutExhaustingSession(t *testing.T) {
	a := &App{}
	dir := t.TempDir()
	first, err := a.grantNativePath("download", filepath.Join(dir, "first"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFileGrants; i++ {
		path := filepath.Join(dir, fmt.Sprintf("file-%d", i))
		if _, err := a.grantNativePath("download", path); err != nil {
			t.Fatal(err)
		}
		if _, ok := a.nativeGrant("download", path); !ok {
			t.Fatal("new selection missing")
		}
	}
	if len(a.fileGrants.entries) != maxFileGrants {
		t.Fatal("unbounded grants")
	}
	if _, ok := a.nativeGrant("download", first); ok {
		t.Fatal("oldest grant retained")
	}
	if _, err := a.grantNativePath("folder", dir); err != nil {
		t.Fatal(err)
	}
}
