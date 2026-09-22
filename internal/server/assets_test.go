package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func testTinyJPEG(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	pixels := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixels.Set(0, 0, color.RGBA{R: 0x7f, G: 0x40, B: 0x20, A: 0xff})
	if err := jpeg.Encode(&encoded, pixels, nil); err != nil {
		t.Fatalf("encode test JPEG: %v", err)
	}
	return encoded.Bytes()
}

func testAnimatedGIF(t *testing.T) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second.SetColorIndex(0, 0, 1)
	var encoded bytes.Buffer
	if err := gif.EncodeAll(&encoded, &gif.GIF{
		Image: []*image.Paletted{first, second},
		Delay: []int{0, 1},
	}); err != nil {
		t.Fatalf("encode animated test GIF: %v", err)
	}
	return encoded.Bytes()
}

func testPNGWithChunk(t *testing.T, chunkType string, data []byte) []byte {
	t.Helper()
	if len(chunkType) != 4 {
		t.Fatalf("PNG chunk type %q must be four bytes", chunkType)
	}
	iend := bytes.LastIndex(tinyPNG, []byte("IEND"))
	if iend < 4 {
		t.Fatal("test PNG has no IEND chunk")
	}
	iend -= 4 // Include IEND's length field in the retained suffix.

	chunk := make([]byte, 12+len(data))
	binary.BigEndian.PutUint32(chunk[:4], uint32(len(data)))
	copy(chunk[4:8], chunkType)
	copy(chunk[8:8+len(data)], data)
	binary.BigEndian.PutUint32(chunk[8+len(data):], crc32.ChecksumIEEE(chunk[4:8+len(data)]))

	raw := make([]byte, 0, len(tinyPNG)+len(chunk))
	raw = append(raw, tinyPNG[:iend]...)
	raw = append(raw, chunk...)
	raw = append(raw, tinyPNG[iend:]...)
	return raw
}

func TestAvatarAssetBaseIsDeterministicAndLocal(t *testing.T) {
	ids := []string{
		"",
		"ordinary-user",
		"teamspeak/id+with=base64",
		"../unix/traversal",
		`..\windows\traversal`,
		`C:\Windows\system32\image`,
		"/etc/secret",
	}
	seen := make(map[string]string, len(ids))
	for _, id := range ids {
		got := avatarAssetBase(id)
		if again := avatarAssetBase(id); again != got {
			t.Fatalf("avatarAssetBase(%q) changed from %q to %q", id, got, again)
		}
		if len(got) != sha256.Size*2 {
			t.Fatalf("avatarAssetBase(%q) length = %d", id, len(got))
		}
		if _, err := hex.DecodeString(got); err != nil {
			t.Fatalf("avatarAssetBase(%q) = %q: %v", id, got, err)
		}
		sum := sha256.Sum256([]byte(id))
		if want := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("avatarAssetBase(%q) = %q, want SHA-256 %q", id, got, want)
		}
		if !filepath.IsLocal(got) || filepath.Base(got) != got || strings.ContainsAny(got, `/\`) {
			t.Fatalf("avatarAssetBase(%q) is not local: %q", id, got)
		}
		if prior, exists := seen[got]; exists {
			t.Fatalf("test IDs %q and %q mapped to the same name", prior, id)
		}
		seen[got] = id
	}
}

func TestEmojiNameValidationIsStrict(t *testing.T) {
	valid := []string{"a", "party-time_2", strings.Repeat("x", 32)}
	for _, name := range valid {
		if !emojiNameRe.MatchString(name) {
			t.Errorf("emoji name %q was rejected", name)
		}
	}
	invalid := []string{
		"", strings.Repeat("x", 33), "Party", "party time", "../party",
		`..\party`, "party.png", ":party:", "fire🔥", "a/b", `a\b`,
	}
	for _, name := range invalid {
		if emojiNameRe.MatchString(name) {
			t.Errorf("emoji name %q was accepted", name)
		}
	}
}

func TestAssetModeSecurityModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		if assetModesEnforceAccess() {
			t.Fatal("POSIX asset modes must not claim to enforce Windows DACLs")
		}
		if len(AssetStorageSecurityWarnings()) < 2 {
			t.Fatal("Windows startup must surface ACL and power-loss limitations")
		}
		t.Log("Windows assets rely on a restricted inheritable FileRoot ACL; os.Chmod mode bits do not rewrite the DACL")
		return
	}
	if !assetModesEnforceAccess() {
		t.Fatal("POSIX asset mode hardening unexpectedly disabled")
	}
	if warnings := AssetStorageSecurityWarnings(); len(warnings) != 0 {
		t.Fatalf("unexpected POSIX asset warnings: %v", warnings)
	}
}

func TestAssetImageValidationRejectsAnimationAndBoundsDecodes(t *testing.T) {
	if err := validateAssetImage(tinyPNG, ".png"); err != nil {
		t.Fatalf("static PNG rejected: %v", err)
	}
	for _, chunkType := range []string{"acTL", "fcTL", "fdAT"} {
		t.Run("animated PNG "+chunkType, func(t *testing.T) {
			raw := testPNGWithChunk(t, chunkType, nil)
			if err := validateAssetImage(raw, ".png"); err == nil || !strings.Contains(err.Error(), "animated PNG") {
				t.Fatalf("animated PNG error = %v, want explicit rejection", err)
			}
		})
	}
	malformedPNG := append([]byte(nil), tinyPNG...)
	binary.BigEndian.PutUint32(malformedPNG[8:12], ^uint32(0))
	if err := validateStaticImageContainer(malformedPNG); err == nil || !strings.Contains(err.Error(), "malformed PNG") {
		t.Fatalf("oversized PNG chunk error = %v, want safe malformed-container rejection", err)
	}

	if err := validateAssetImage(tinyGIF, ".gif"); err != nil {
		t.Fatalf("static GIF rejected: %v", err)
	}
	if err := validateAssetImage(testAnimatedGIF(t), ".gif"); err == nil || !strings.Contains(err.Error(), "animated GIF") {
		t.Fatalf("animated GIF error = %v, want explicit rejection", err)
	}
	animatedWebPHeader := make([]byte, 30)
	copy(animatedWebPHeader[:4], "RIFF")
	binary.LittleEndian.PutUint32(animatedWebPHeader[4:8], uint32(len(animatedWebPHeader)-8))
	copy(animatedWebPHeader[8:12], "WEBP")
	copy(animatedWebPHeader[12:16], "VP8X")
	binary.LittleEndian.PutUint32(animatedWebPHeader[16:20], 10)
	animatedWebPHeader[20] = 0x02
	if err := validateStaticImageContainer(animatedWebPHeader); err == nil || !strings.Contains(err.Error(), "animated WebP") {
		t.Fatalf("animated WebP error = %v, want explicit rejection", err)
	}
	if got := cap(assetImageDecodeSlots); got != maxConcurrentImageDecodes {
		t.Fatalf("production decode slots = %d, want %d", got, maxConcurrentImageDecodes)
	}

	slots := make(chan struct{}, 1)
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := withAssetImageDecodeSlot(slots, func() (assetImageFormat, error) {
			close(firstEntered)
			<-releaseFirst
			return assetImageFormat{}, nil
		})
		firstDone <- err
	}()
	<-firstEntered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		_, err := withAssetImageDecodeSlot(slots, func() (assetImageFormat, error) {
			close(secondEntered)
			return assetImageFormat{}, nil
		})
		secondDone <- err
	}()
	select {
	case <-secondEntered:
		close(releaseFirst)
		t.Fatal("second decode entered while the only slot was occupied")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first decode slot: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second decode slot: %v", err)
	}
}

func TestAssetStorageMigratesSafeLegacyAvatar(t *testing.T) {
	rootDir := t.TempDir()
	avatarDir := filepath.Join(rootDir, "avatars")
	if err := os.Mkdir(avatarDir, 0o777); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"user-uid.png": tinyPNG, "user-uid.jpg": []byte("duplicate")} {
		if err := os.WriteFile(filepath.Join(avatarDir, name), data, 0o666); err != nil {
			t.Fatal(err)
		}
	}

	storage := assetStorage{rootDir: rootDir}
	raw, image, err := storage.readAvatar("user-uid")
	if err != nil {
		t.Fatalf("readAvatar: %v", err)
	}
	if !bytes.Equal(raw, tinyPNG) || image.fileName != avatarAssetBase("user-uid")+".png" {
		t.Fatalf("migrated avatar = %q from %q", raw, image.fileName)
	}
	for _, legacy := range []string{"user-uid.png", "user-uid.jpg"} {
		if _, err := os.Stat(filepath.Join(avatarDir, legacy)); !os.IsNotExist(err) {
			t.Fatalf("legacy avatar %q remains: %v", legacy, err)
		}
	}
	migrated := filepath.Join(avatarDir, image.fileName)
	if got, err := os.ReadFile(migrated); err != nil || !bytes.Equal(got, tinyPNG) {
		t.Fatalf("migrated file = %q, err = %v", got, err)
	}
	if assetModesEnforceAccess() {
		for path, want := range map[string]os.FileMode{
			rootDir:   assetDirMode,
			avatarDir: assetDirMode,
			migrated:  assetFileMode,
		} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Fatalf("%s mode = %o, want %o", path, got, want)
			}
		}
	}
}

func TestAssetStorageDoesNotFallbackUnsafeLegacyAvatar(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootDir, "avatars"), 0o700); err != nil {
		t.Fatal(err)
	}
	outsideLegacy := filepath.Join(rootDir, "legacy.png")
	if err := os.WriteFile(outsideLegacy, []byte("must-not-read"), 0o600); err != nil {
		t.Fatal(err)
	}

	storage := assetStorage{rootDir: rootDir}
	if _, _, err := storage.readAvatar("../legacy"); !os.IsNotExist(err) {
		t.Fatalf("unsafe legacy lookup error = %v, want not found", err)
	}
	if got, err := os.ReadFile(outsideLegacy); err != nil || string(got) != "must-not-read" {
		t.Fatalf("unsafe legacy target = %q, err = %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "avatars", avatarAssetBase("../legacy")+".png")); !os.IsNotExist(err) {
		t.Fatalf("unsafe legacy avatar was migrated: %v", err)
	}
}

func TestAssetStorageAvatarMigrationSerializesConcurrentSet(t *testing.T) {
	rootDir := t.TempDir()
	avatarDir := filepath.Join(rootDir, "avatars")
	if err := os.Mkdir(avatarDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(avatarDir, "user-uid.png"), tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	storage := assetStorage{rootDir: rootDir}
	legacyRead := make(chan struct{})
	setterAttempted := make(chan struct{})
	allowMigration := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		_, _, err := storage.readAvatarWithHook("user-uid", func() {
			close(legacyRead)
			<-allowMigration
		})
		readDone <- err
	}()
	<-legacyRead
	setDone := make(chan error, 1)
	go func() {
		close(setterAttempted)
		_, err := storage.writeAvatar("user-uid", ".gif", tinyGIF)
		setDone <- err
	}()
	<-setterAttempted
	close(allowMigration)
	if err := <-readDone; err != nil {
		t.Fatalf("migration: %v", err)
	}
	if err := <-setDone; err != nil {
		t.Fatalf("set: %v", err)
	}
	raw, _, err := storage.readAvatar("user-uid")
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	if !bytes.Equal(raw, tinyGIF) {
		t.Fatalf("concurrent set was overwritten by stale migration: %q", raw)
	}
}

func TestAssetStorageAtomicReplaceAndModes(t *testing.T) {
	rootDir := t.TempDir()
	storage := assetStorage{rootDir: rootDir}

	fileName, err := storage.writeImage("emojis", "party", ".png", tinyPNG)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if fileName != "party.png" {
		t.Fatalf("file name = %q", fileName)
	}
	if _, err := storage.writeImage("emojis", "party", ".png", tinyPNG); err != nil {
		t.Fatalf("same-extension replacement: %v", err)
	}
	if _, err := storage.writeImage("emojis", "party", ".gif", tinyGIF); err != nil {
		t.Fatalf("format replacement: %v", err)
	}

	raw, image, err := storage.readImage("emojis", "party")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(raw, tinyGIF) || image.fileName != "party.gif" {
		t.Fatalf("read = %q from %q", raw, image.fileName)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "emojis", "party.png")); !os.IsNotExist(err) {
		t.Fatalf("superseded extension still exists: %v", err)
	}
	if leftovers, err := filepath.Glob(filepath.Join(rootDir, "emojis", ".*.tmp")); err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files = %v, err = %v", leftovers, err)
	}

	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(filepath.Join(rootDir, "emojis"))
		if err != nil {
			t.Fatal(err)
		}
		if got := dirInfo.Mode().Perm(); got != assetDirMode {
			t.Fatalf("directory mode = %o, want %o", got, assetDirMode)
		}
		fileInfo, err := os.Stat(filepath.Join(rootDir, "emojis", "party.gif"))
		if err != nil {
			t.Fatal(err)
		}
		if got := fileInfo.Mode().Perm(); got != assetFileMode {
			t.Fatalf("file mode = %o, want %o", got, assetFileMode)
		}
	}

	renamed, err := storage.renameImage("emojis", "party", "celebrate")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed != "celebrate.gif" {
		t.Fatalf("renamed file = %q", renamed)
	}
	images, err := storage.listImages("emojis")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(images) != 1 || images[0].base != "celebrate" {
		t.Fatalf("images = %+v", images)
	}
	if _, err := storage.removeImage("emojis", "celebrate"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, _, err := storage.readImage("emojis", "celebrate"); !os.IsNotExist(err) {
		t.Fatalf("removed image read error = %v", err)
	}
}

func TestReadRegularAssetRejectsIdentitySwap(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "image.png"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "replacement.png"), []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	opener := func(name string) (*os.File, error) {
		if err := root.Remove(name); err != nil {
			return nil, err
		}
		if err := root.Rename("replacement.png", name); err != nil {
			return nil, err
		}
		return root.Open(name)
	}
	if _, err := readRegularAssetWithOpener(root, "image.png", 1024, opener); !errors.Is(err, errUnsafeAsset) {
		t.Fatalf("identity swap error = %v, want errUnsafeAsset", err)
	}
}

func TestAssetStorageOpenRootKeepsVerifiedHandle(t *testing.T) {
	parent := t.TempDir()
	rootDir := filepath.Join(parent, "assets")
	if err := os.Mkdir(rootDir, assetDirMode); err != nil {
		t.Fatal(err)
	}
	storage := assetStorage{rootDir: rootDir}

	t.Run("opens pathname once", func(t *testing.T) {
		calls := 0
		root, err := storage.openRootWithOpener(func(path string) (*os.Root, error) {
			calls++
			return os.OpenRoot(path)
		})
		if err != nil {
			t.Fatalf("openRootWithOpener: %v", err)
		}
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("root pathname opened %d times, want exactly once", calls)
		}
	})

	t.Run("path swap after open cannot redirect handle", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows does not permit renaming this opened directory handle")
		}
		moved := filepath.Join(parent, "verified-root")
		calls := 0
		root, err := storage.openRootWithOpener(func(path string) (*os.Root, error) {
			calls++
			opened, err := os.OpenRoot(path)
			if err != nil {
				return nil, err
			}
			if err := os.Rename(path, moved); err != nil {
				_ = opened.Close()
				return nil, err
			}
			if err := os.Mkdir(path, assetDirMode); err != nil {
				_ = opened.Close()
				return nil, err
			}
			return opened, nil
		})
		if err != nil {
			t.Fatalf("open verified root across swap: %v", err)
		}
		defer func() { _ = root.Close() }()
		if calls != 1 {
			t.Fatalf("root pathname opened %d times, want exactly once", calls)
		}
		if _, err := writeImageAtRoot(root, "icons", "1", ".png", tinyPNG); err != nil {
			t.Fatalf("write through verified handle: %v", err)
		}
		if _, err := os.Stat(filepath.Join(moved, "icons", "1.png")); err != nil {
			t.Fatalf("verified directory did not receive write: %v", err)
		}
		if _, err := os.Stat(filepath.Join(rootDir, "icons", "1.png")); !os.IsNotExist(err) {
			t.Fatalf("replacement pathname received redirected write: %v", err)
		}
	})
}

func TestAssetStorageNormalizesExistingModes(t *testing.T) {
	rootDir := t.TempDir()
	assetDir := filepath.Join(rootDir, "emojis")
	if err := os.Mkdir(assetDir, 0o777); err != nil {
		t.Fatal(err)
	}
	assetPath := filepath.Join(assetDir, "party.png")
	if err := os.WriteFile(assetPath, tinyPNG, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(rootDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(assetDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(assetPath, 0o666); err != nil {
		t.Fatal(err)
	}

	storage := assetStorage{rootDir: rootDir}
	if _, _, err := storage.readImage("emojis", "party"); err != nil {
		t.Fatalf("read existing asset: %v", err)
	}
	if !assetModesEnforceAccess() {
		return
	}
	for path, want := range map[string]os.FileMode{
		rootDir:   assetDirMode,
		assetDir:  assetDirMode,
		assetPath: assetFileMode,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestAssetStorageCollapsesMultiExtensionOperations(t *testing.T) {
	rootDir := t.TempDir()
	dir := filepath.Join(rootDir, "emojis")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"party.jpg": testTinyJPEG(t), "party.png": tinyPNG, "other.gif": tinyGIF,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	storage := assetStorage{rootDir: rootDir}
	images, err := storage.listImages("emojis")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(images) != 2 {
		t.Fatalf("logical image count = %d, want 2: %+v", len(images), images)
	}
	var party assetImage
	for _, image := range images {
		if image.base == "party" {
			party = image
		}
	}
	if party.fileName != "party.png" {
		t.Fatalf("chosen party variant = %q, want deterministic PNG", party.fileName)
	}

	renamed, err := storage.renameImage("emojis", "party", "celebrate")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed != "celebrate.png" {
		t.Fatalf("renamed = %q", renamed)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, "party.*")); err != nil || len(matches) != 0 {
		t.Fatalf("old variants after rename = %v, err = %v", matches, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "celebrate.jpg"), []byte("duplicate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := storage.removeImage("emojis", "celebrate"); err != nil || removed != "celebrate.png" {
		t.Fatalf("remove = %q, err = %v", removed, err)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, "celebrate.*")); err != nil || len(matches) != 0 {
		t.Fatalf("variants after delete = %v, err = %v", matches, err)
	}
}

func TestAssetStorageSkipsInvalidPreferredCandidates(t *testing.T) {
	rootDir := t.TempDir()
	dir := filepath.Join(rootDir, "emojis")
	if err := os.Mkdir(dir, assetDirMode); err != nil {
		t.Fatal(err)
	}
	jpegData := testTinyJPEG(t)
	oversized := make([]byte, maxImageBytes+1)
	copy(oversized, tinyPNG)
	files := map[string][]byte{
		"party.png":    []byte("corrupt preferred candidate"),
		"party.jpg":    jpegData,
		"fallback.png": oversized,
		"fallback.gif": tinyGIF,
		"broken.png":   tinyPNG[:24],
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, assetFileMode); err != nil {
			t.Fatal(err)
		}
	}

	storage := assetStorage{rootDir: rootDir}
	raw, selected, err := storage.readImage("emojis", "party")
	if err != nil || selected.fileName != "party.jpg" || !bytes.Equal(raw, jpegData) {
		t.Fatalf("party fallback = %q (%d bytes), err = %v", selected.fileName, len(raw), err)
	}
	images, err := storage.listImages("emojis")
	if err != nil {
		t.Fatalf("list images: %v", err)
	}
	got := make([]string, 0, len(images))
	for _, image := range images {
		got = append(got, image.fileName)
	}
	if fmt.Sprint(got) != "[fallback.gif party.jpg]" {
		t.Fatalf("valid listed candidates = %v, want [fallback.gif party.jpg]", got)
	}

	renamed, err := storage.renameImage("emojis", "party", "celebrate")
	if err != nil || renamed != "celebrate.jpg" {
		t.Fatalf("rename valid fallback = %q, err = %v", renamed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "party.png")); !os.IsNotExist(err) {
		t.Fatalf("corrupt source variant survived rename: %v", err)
	}
	raw, selected, err = storage.readImage("emojis", "celebrate")
	if err != nil || selected.fileName != "celebrate.jpg" || !bytes.Equal(raw, jpegData) {
		t.Fatalf("renamed fallback = %q (%d bytes), err = %v", selected.fileName, len(raw), err)
	}
}

func TestDiscoverChannelIconIDsFiltersAndNormalizes(t *testing.T) {
	rootDir := t.TempDir()
	dir := filepath.Join(rootDir, "icons")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"1.png":   tinyPNG,
		"1.jpg":   tinyPNG, // decoded PNG does not match the extension
		"2.gif":   tinyGIF,
		"03.png":  tinyPNG,
		"-4.gif":  tinyGIF,
		"bad.png": tinyPNG,
		"5.PNG":   tinyPNG,
		"6.txt":   []byte("unsupported"),
		"9.png":   tinyPNG[:24], // sniffable header, incomplete image
	}
	oversized := make([]byte, maxImageBytes+1)
	copy(oversized, tinyPNG)
	files["10.png"] = oversized
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "7.png"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o644); err != nil {
		t.Fatal(err)
	}
	linked := os.Symlink(outside, filepath.Join(dir, "8.png")) == nil

	ids, err := DiscoverChannelIconIDs(rootDir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if fmt.Sprint(ids) != "[1 2]" {
		t.Fatalf("IDs = %v, want [1 2]", ids)
	}
	if assetModesEnforceAccess() {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != assetDirMode {
			t.Fatalf("icons directory mode = %o", got)
		}
		for _, name := range []string{"1.png", "1.jpg", "2.gif"} {
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != assetFileMode {
				t.Fatalf("%s mode = %o", name, got)
			}
		}
		if linked {
			info, err := os.Stat(outside)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o644 {
				t.Fatalf("symlink target mode changed to %o", got)
			}
		}
	}
}

func TestAssetStorageRejectsInvalidServedImages(t *testing.T) {
	rootDir := t.TempDir()
	dir := filepath.Join(rootDir, "icons")
	if err := os.Mkdir(dir, assetDirMode); err != nil {
		t.Fatal(err)
	}
	oversized := make([]byte, maxImageBytes+1)
	copy(oversized, tinyPNG)
	files := map[string][]byte{
		"1.png": tinyPNG,
		"2.png": tinyPNG[:24],
		"3.jpg": tinyPNG,
		"4.png": oversized,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, assetFileMode); err != nil {
			t.Fatal(err)
		}
	}
	storage := assetStorage{rootDir: rootDir}
	if raw, _, err := storage.readImage("icons", "1"); err != nil || !bytes.Equal(raw, tinyPNG) {
		t.Fatalf("valid served image = %d bytes, err = %v", len(raw), err)
	}
	for _, base := range []string{"2", "3", "4"} {
		if _, _, err := storage.readImage("icons", base); err == nil {
			t.Errorf("invalid served image %s was accepted", base)
		}
	}
}

func TestAssetStorageRejectsTraversalComponents(t *testing.T) {
	storage := assetStorage{rootDir: t.TempDir()}
	for _, base := range []string{"../escape", `..\escape`, "/absolute", `C:\absolute`, "a/b", `a\b`, ".", ".."} {
		if _, err := storage.writeImage("avatars", base, ".png", []byte("x")); !errors.Is(err, errUnsafeAsset) {
			t.Errorf("writeImage base %q error = %v, want errUnsafeAsset", base, err)
		}
	}
	for _, dir := range []string{"../avatars", `..\avatars`, "/avatars", `C:\avatars`, "nested/avatars"} {
		if _, err := storage.writeImage(dir, "safe", ".png", []byte("x")); !errors.Is(err, errUnsafeAsset) {
			t.Errorf("writeImage dir %q error = %v, want errUnsafeAsset", dir, err)
		}
	}
}

func TestAssetStorageRejectsSymlinkEscape(t *testing.T) {
	rootDir := t.TempDir()
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "outside.png")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outsidePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootDir, "avatars"), assetDirMode); err != nil {
		t.Fatal(err)
	}
	base := avatarAssetBase("victim")
	linkPath := filepath.Join(rootDir, "avatars", base+".png")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("symlink creation unavailable on %s: %v", runtime.GOOS, err)
	}

	storage := assetStorage{rootDir: rootDir}
	if _, _, err := storage.readImage("avatars", base); !errors.Is(err, errUnsafeAsset) {
		t.Fatalf("symlink read error = %v, want errUnsafeAsset", err)
	}
	if _, err := storage.writeImage("avatars", base, ".png", []byte("overwrite")); !errors.Is(err, errUnsafeAsset) {
		t.Fatalf("symlink write error = %v, want errUnsafeAsset", err)
	}
	outside, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(outside) != "outside" {
		t.Fatalf("outside file changed to %q", outside)
	}
	if assetModesEnforceAccess() {
		info, err := os.Stat(outsidePath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Fatalf("symlink target mode changed to %o", got)
		}
	}
}

func TestAssetStorageRejectsNonRegularFile(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootDir, "icons"), assetDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootDir, "icons", "1.png"), assetDirMode); err != nil {
		t.Fatal(err)
	}
	storage := assetStorage{rootDir: rootDir}
	if _, _, err := storage.readImage("icons", "1"); !errors.Is(err, errUnsafeAsset) {
		t.Fatalf("directory read error = %v, want errUnsafeAsset", err)
	}
	if _, err := storage.writeImage("icons", "1", ".png", []byte("x")); !errors.Is(err, errUnsafeAsset) {
		t.Fatalf("directory write error = %v, want errUnsafeAsset", err)
	}
}

func TestAssetStorageConcurrentFormatReplace(t *testing.T) {
	storage := assetStorage{rootDir: t.TempDir()}
	const writers = 24
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		extension := ".png"
		if i%2 != 0 {
			extension = ".gif"
		}
		wg.Add(1)
		go func(index int, ext string) {
			defer wg.Done()
			<-start
			data := tinyPNG
			if ext == ".gif" {
				data = tinyGIF
			}
			_, err := storage.writeImage("emojis", "party", ext, data)
			errs <- err
		}(i, extension)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}

	images, err := storage.listImages("emojis")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(images) != 1 || images[0].base != "party" {
		t.Fatalf("images after concurrent writes = %+v", images)
	}
	raw, _, err := storage.readImage("emojis", "party")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(raw, tinyPNG) && !bytes.Equal(raw, tinyGIF) {
		t.Fatalf("stored data is not one of the complete test images")
	}
}

func TestAssetStorageLogicalLockEntriesAreEvictedAfterChurn(t *testing.T) {
	storage := assetStorage{rootDir: t.TempDir()}
	locks := storage.lockSet()
	const requests = 512

	start := make(chan struct{})
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for index := 0; index < requests; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, _, err := storage.readImage("emojis", fmt.Sprintf("missing-%d", index))
			if !errors.Is(err, os.ErrNotExist) {
				errs <- err
			}
		}(index)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("missing asset read: %v", err)
	}

	locks.namespaces.mu.Lock()
	namespaceEntries := len(locks.namespaces.entries)
	locks.namespaces.mu.Unlock()
	locks.bases.mu.Lock()
	baseEntries := len(locks.bases.entries)
	locks.bases.mu.Unlock()
	if namespaceEntries != 0 || baseEntries != 0 {
		t.Fatalf("logical lock registry retained namespace=%d base=%d entries", namespaceEntries, baseEntries)
	}
}

func TestAssetStorageLogicalLockWaitersKeepOneIdentity(t *testing.T) {
	storage := assetStorage{rootDir: t.TempDir()}
	locks := storage.lockSet()
	const key = "emojis\x00shared"

	entryWithRefs := func(want int) *assetLockEntry {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			locks.bases.mu.Lock()
			entry := locks.bases.entries[key]
			refs := 0
			if entry != nil {
				refs = entry.refs
			}
			locks.bases.mu.Unlock()
			if refs == want {
				return entry
			}
			if time.Now().After(deadline) {
				t.Fatalf("base lock refs = %d, want %d", refs, want)
			}
			runtime.Gosched()
		}
	}

	unlockFirst := storage.lockAssets("emojis", true, "shared")
	firstEntry := entryWithRefs(1)
	secondAcquired := make(chan struct{})
	releaseSecond := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		unlock := storage.lockAssets("emojis", true, "shared")
		close(secondAcquired)
		<-releaseSecond
		unlock()
		close(secondDone)
	}()
	if entry := entryWithRefs(2); entry != firstEntry {
		t.Fatal("waiter registered under a different logical lock identity")
	}
	select {
	case <-secondAcquired:
		unlockFirst()
		close(releaseSecond)
		<-secondDone
		t.Fatal("second writer acquired while the first writer held the lock")
	case <-time.After(50 * time.Millisecond):
	}

	unlockFirst()
	select {
	case <-secondAcquired:
	case <-time.After(3 * time.Second):
		t.Fatal("second writer did not acquire after first release")
	}
	if entry := entryWithRefs(1); entry != firstEntry {
		t.Fatal("lock identity changed while the second writer held it")
	}

	thirdAcquired := make(chan struct{})
	releaseThird := make(chan struct{})
	thirdDone := make(chan struct{})
	go func() {
		unlock := storage.lockAssets("emojis", true, "shared")
		close(thirdAcquired)
		<-releaseThird
		unlock()
		close(thirdDone)
	}()
	if entry := entryWithRefs(2); entry != firstEntry {
		t.Fatal("later waiter registered under a different logical lock identity")
	}
	select {
	case <-thirdAcquired:
		close(releaseSecond)
		close(releaseThird)
		t.Fatal("third writer acquired while the second writer held the lock")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseSecond)
	<-secondDone
	select {
	case <-thirdAcquired:
	case <-time.After(3 * time.Second):
		t.Fatal("third writer did not acquire after second release")
	}
	close(releaseThird)
	<-thirdDone

	locks.namespaces.mu.Lock()
	namespaceEntries := len(locks.namespaces.entries)
	locks.namespaces.mu.Unlock()
	locks.bases.mu.Lock()
	baseEntries := len(locks.bases.entries)
	locks.bases.mu.Unlock()
	if namespaceEntries != 0 || baseEntries != 0 {
		t.Fatalf("logical lock registry retained namespace=%d base=%d entries", namespaceEntries, baseEntries)
	}
}
