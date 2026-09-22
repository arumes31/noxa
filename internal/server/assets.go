package server

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	_ "golang.org/x/image/webp"

	"noxa/internal/safecast"
)

const (
	assetDirMode                    = 0o700
	assetFileMode                   = 0o600
	maxImageDimension               = 4096
	maxImagePixels            int64 = 4 * 1024 * 1024
	maxConcurrentImageDecodes       = 4
)

var (
	errUnsafeAsset        = errors.New("unsafe asset path")
	assetImageDecodeSlots = make(chan struct{}, maxConcurrentImageDecodes)
	staticPNGSignature    = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
)

// assetStorageLocks owns a lock set per configured FileRoot. Operations first
// take a shared namespace lock, then sorted logical-base locks. That lets
// unrelated assets proceed independently while directory scans can take the
// namespace exclusively for a coherent snapshot. noXa requires one writer
// process per FileRoot: no cross-process lock coordinates extension collapse
// or asset variants.
var assetStorageLocks sync.Map // map[string]*assetLockSet

type assetLockSet struct {
	namespaces assetLockTable
	bases      assetLockTable
}

// assetLockTable evicts an identity after its final holder or waiter releases
// it. Waiters increment refs before blocking on the RWMutex, preventing an ABA
// split where a new lock could be installed while an older waiter still owns
// or is about to own the same logical identity.
type assetLockTable struct {
	mu      sync.Mutex
	entries map[string]*assetLockEntry
}

type assetLockEntry struct {
	mu   sync.RWMutex
	refs int
}

type assetImageFormat struct {
	contentType string
	extension   string
	decoderName string
}

var assetImageFormats = []assetImageFormat{
	{contentType: "image/png", extension: ".png", decoderName: "png"},
	{contentType: "image/jpeg", extension: ".jpg", decoderName: "jpeg"},
	{contentType: "image/gif", extension: ".gif", decoderName: "gif"},
	{contentType: "image/webp", extension: ".webp", decoderName: "webp"},
}

type assetImage struct {
	base        string
	fileName    string
	contentType string
	extension   string
}

type assetImageSnapshot struct {
	image assetImage
	data  []byte
}

type assetStorage struct {
	rootDir string
}

type assetFileOpener func(string) (*os.File, error)
type assetRootOpener func(string) (*os.Root, error)

type assetMutationOps struct {
	remove  func(*os.Root, string) error
	syncDir func(*os.Root, string) error
	restore func(*os.Root, string, string, []assetImageSnapshot) error
}

func defaultAssetMutationOps() assetMutationOps {
	return assetMutationOps{
		remove:  func(root *os.Root, rel string) error { return root.Remove(rel) },
		syncDir: syncAssetDir,
		restore: restoreImageVariants,
	}
}

func (s *TCPServer) assets() assetStorage {
	return assetStorage{rootDir: s.cfg.FileRoot}
}

// avatarAssetBase maps arbitrary TeamSpeak-style unique IDs (which may contain
// '/', '+', and '=') to one deterministic cross-platform file name.
func avatarAssetBase(uniqueID string) string {
	sum := sha256.Sum256([]byte(uniqueID))
	return hex.EncodeToString(sum[:])
}

func (s assetStorage) lockSet() *assetLockSet {
	key := filepath.Clean(s.rootDir)
	if absolute, err := filepath.Abs(key); err == nil {
		key = absolute
	}
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	locks, _ := assetStorageLocks.LoadOrStore(key, new(assetLockSet))
	return locks.(*assetLockSet)
}

func (t *assetLockTable) lock(key string, write bool) func() {
	t.mu.Lock()
	if t.entries == nil {
		t.entries = make(map[string]*assetLockEntry)
	}
	entry := t.entries[key]
	if entry == nil {
		entry = new(assetLockEntry)
		t.entries[key] = entry
	}
	entry.refs++
	t.mu.Unlock()

	if write {
		entry.mu.Lock()
	} else {
		entry.mu.RLock()
	}
	return func() {
		if write {
			entry.mu.Unlock()
		} else {
			entry.mu.RUnlock()
		}
		t.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(t.entries, key)
		}
		t.mu.Unlock()
	}
}

// lockAssets acquires the namespace before sorted logical-base locks. All
// multi-key operations use this order, so avatar migration and rename cannot
// deadlock each other. The returned function releases in reverse order.
func (s assetStorage) lockAssets(dir string, write bool, bases ...string) func() {
	locks := s.lockSet()
	unlockNamespace := locks.namespaces.lock(dir, false)

	unique := make(map[string]struct{}, len(bases))
	for _, base := range bases {
		unique[base] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for base := range unique {
		ordered = append(ordered, base)
	}
	sort.Strings(ordered)

	unlocks := make([]func(), 0, len(ordered))
	for _, base := range ordered {
		unlocks = append(unlocks, locks.bases.lock(dir+"\x00"+base, write))
	}
	return func() {
		for index := len(unlocks) - 1; index >= 0; index-- {
			unlocks[index]()
		}
		unlockNamespace()
	}
}

func (s assetStorage) lockNamespace(dir string) func() {
	return s.lockSet().namespaces.lock(dir, true)
}

// assetModesEnforceAccess reports whether 0700/0600 mode bits express access
// control on this platform. Windows' os.Chmod does not rewrite NTFS DACLs, so
// operators must provision FileRoot with a restricted inheritable ACL; child
// assets inherit that ACL even though Go still records best-effort mode bits.
func assetModesEnforceAccess() bool {
	return runtime.GOOS != "windows"
}

// AssetStorageSecurityWarnings returns platform limitations that require an
// operator decision. The standard library cannot prove that a Windows
// FileRoot DACL is restricted and inheritable, so starting silently would
// overstate the protection provided by best-effort 0700/0600 mode bits.
func AssetStorageSecurityWarnings() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	return []string{
		"Windows os.Chmod does not restrict NTFS DACLs: provision FileRoot with a restricted inheritable ACL before serving untrusted assets",
		"Windows directory fsync is unavailable in this asset path: transaction recovery is best-effort across sudden power loss",
	}
}

func (s assetStorage) openRoot() (*os.Root, error) {
	return s.openRootWithOpener(os.OpenRoot)
}

func (s assetStorage) openRootWithOpener(opener assetRootOpener) (*os.Root, error) {
	if strings.TrimSpace(s.rootDir) == "" {
		return nil, errors.New("asset root is empty")
	}
	if opener == nil {
		return nil, errors.New("asset root opener is nil")
	}
	if err := os.MkdirAll(s.rootDir, assetDirMode); err != nil {
		return nil, fmt.Errorf("creating asset root: %w", err)
	}
	before, err := os.Lstat(s.rootDir)
	if err != nil {
		return nil, fmt.Errorf("inspecting asset root: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, fmt.Errorf("%w: asset root is not a real directory", errUnsafeAsset)
	}
	root, err := opener(s.rootDir)
	if err != nil {
		return nil, fmt.Errorf("opening asset root: %w", err)
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	opened, err := directory.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspecting opened asset root: %w", err), directory.Close(), root.Close())
	}
	if !opened.IsDir() || !os.SameFile(before, opened) {
		return nil, errors.Join(
			fmt.Errorf("%w: asset root changed while opening", errUnsafeAsset),
			directory.Close(),
			root.Close(),
		)
	}
	if err := directory.Chmod(assetDirMode); err != nil {
		return nil, errors.Join(fmt.Errorf("restricting asset root permissions: %w", err), directory.Close(), root.Close())
	}
	if err := directory.Close(); err != nil {
		return nil, errors.Join(fmt.Errorf("closing asset root after permission normalization: %w", err), root.Close())
	}
	// Return the same os.Root whose directory handle was compared above. Never
	// close and reopen by pathname: an attacker could swap that path between
	// verification and the first confined operation.
	return root, nil
}

func (s assetStorage) writeImage(dir, base, extension string, data []byte) (string, error) {
	if _, err := assetImagePath(dir, base, extension); err != nil {
		return "", err
	}
	unlock := s.lockAssets(dir, true, base)
	defer unlock()

	root, err := s.openRoot()
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	return writeImageAtRoot(root, dir, base, extension, data)
}

func writeImageAtRoot(root *os.Root, dir, base, extension string, data []byte) (string, error) {
	return writeImageAtRootWithOps(root, dir, base, extension, data, defaultAssetMutationOps())
}

func writeImageAtRootWithOps(
	root *os.Root,
	dir, base, extension string,
	data []byte,
	ops assetMutationOps,
) (string, error) {
	rel, err := assetImagePath(dir, base, extension)
	if err != nil {
		return "", err
	}
	if ops.remove == nil || ops.syncDir == nil {
		return "", errors.New("asset mutation operation is nil")
	}
	restore := ops.restore
	if restore == nil {
		restore = restoreImageVariants
	}
	prior, err := snapshotImageVariants(root, dir, base)
	if err != nil {
		return "", err
	}
	fileName, err := mutateImageAtRoot(root, dir, base, extension, rel, data, ops)
	if err == nil {
		return fileName, nil
	}
	mutationErr := err
	if rollbackErr := restoreImageVariantsAndVerify(root, dir, base, prior, restore); rollbackErr != nil {
		return "", errors.Join(mutationErr, fmt.Errorf("rolling back failed asset write: %w", rollbackErr))
	}
	return "", mutationErr
}

func mutateImageAtRoot(
	root *os.Root,
	dir, base, extension, rel string,
	data []byte,
	ops assetMutationOps,
) (string, error) {
	if err := ensureAssetDir(root, dir); err != nil {
		return "", err
	}
	existing, err := imageVariants(root, dir, base)
	if err != nil {
		return "", err
	}
	if err := writeAssetAtomicWithSync(root, rel, data, ops.syncDir); err != nil {
		return "", err
	}
	for _, image := range existing {
		if image.extension == extension {
			continue
		}
		if removeErr := ops.remove(root, filepath.Join(dir, image.fileName)); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			return "", fmt.Errorf("removing superseded asset: %w", removeErr)
		}
	}
	if err := ops.syncDir(root, dir); err != nil {
		return "", err
	}
	return filepath.Base(rel), nil
}

// writeAvatar stores the hashed name and removes any safe legacy raw-ID
// variants while holding the same root lock used by lazy migration.
func (s assetStorage) writeAvatar(uniqueID, extension string, data []byte) (string, error) {
	base := avatarAssetBase(uniqueID)
	if _, err := assetImagePath("avatars", base, extension); err != nil {
		return "", err
	}
	bases := []string{base}
	if uniqueID != base && validateAssetComponent("legacy avatar name", uniqueID) == nil {
		bases = append(bases, uniqueID)
	}
	unlock := s.lockAssets("avatars", true, bases...)
	defer unlock()

	root, err := s.openRoot()
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	legacy, err := safeLegacyAvatarVariants(root, uniqueID, base)
	if err != nil {
		return "", err
	}
	fileName, err := writeImageAtRoot(root, "avatars", base, extension, data)
	if err != nil {
		return "", err
	}
	if err := removeKnownVariants(root, "avatars", legacy); err != nil {
		return "", err
	}
	return fileName, nil
}

func (s assetStorage) readImage(dir, base string) ([]byte, assetImage, error) {
	if err := validateAssetComponent("asset name", base); err != nil {
		return nil, assetImage{}, err
	}
	unlock := s.lockAssets(dir, false, base)
	defer unlock()

	root, err := s.openRoot()
	if err != nil {
		return nil, assetImage{}, err
	}
	defer func() { _ = root.Close() }()
	return readImageAtRoot(root, dir, base)
}

func readImageAtRoot(root *os.Root, dir, base string) ([]byte, assetImage, error) {
	image, err := findAssetImage(root, dir, base)
	if err != nil {
		return nil, assetImage{}, err
	}
	raw, err := readRegularAsset(root, filepath.Join(dir, image.fileName), maxImageBytes)
	if err != nil {
		return nil, assetImage{}, err
	}
	if err := validateAssetImage(raw, image.extension); err != nil {
		return nil, assetImage{}, fmt.Errorf("validating asset %q: %w", image.fileName, err)
	}
	return raw, image, nil
}

// readAvatar reads the hashed name or lazily migrates a safe legacy raw-ID
// name. The write lock makes migration linearizable with a concurrent set.
func (s assetStorage) readAvatar(uniqueID string) ([]byte, assetImage, error) {
	return s.readAvatarWithHook(uniqueID, nil)
}

func (s assetStorage) readAvatarWithHook(uniqueID string, afterLegacyRead func()) ([]byte, assetImage, error) {
	base := avatarAssetBase(uniqueID)
	bases := []string{base}
	if uniqueID != base && validateAssetComponent("legacy avatar name", uniqueID) == nil {
		bases = append(bases, uniqueID)
	}
	unlock := s.lockAssets("avatars", true, bases...)
	defer unlock()

	root, err := s.openRoot()
	if err != nil {
		return nil, assetImage{}, err
	}
	defer func() { _ = root.Close() }()
	hashed, err := validImageVariants(root, "avatars", base)
	if err != nil {
		return nil, assetImage{}, err
	}
	if len(hashed) > 0 {
		raw, err := readRegularAsset(root, filepath.Join("avatars", hashed[0].fileName), maxImageBytes)
		if err != nil {
			return nil, assetImage{}, err
		}
		if err := validateAssetImage(raw, hashed[0].extension); err != nil {
			return nil, assetImage{}, fmt.Errorf("validating avatar %q: %w", hashed[0].fileName, err)
		}
		legacy, err := safeLegacyAvatarVariants(root, uniqueID, base)
		if err != nil {
			return nil, assetImage{}, err
		}
		if err := removeKnownVariants(root, "avatars", legacy); err != nil {
			return nil, assetImage{}, err
		}
		return raw, hashed[0], nil
	}
	if validateAssetComponent("legacy avatar name", uniqueID) != nil || uniqueID == base {
		return nil, assetImage{}, fs.ErrNotExist
	}
	legacy, err := validImageVariants(root, "avatars", uniqueID)
	if err != nil {
		return nil, assetImage{}, err
	}
	if len(legacy) == 0 {
		return nil, assetImage{}, fs.ErrNotExist
	}
	legacyFiles, err := existingImageVariants(root, "avatars", uniqueID)
	if err != nil {
		return nil, assetImage{}, err
	}
	raw, err := readRegularAsset(root, filepath.Join("avatars", legacy[0].fileName), maxImageBytes)
	if err != nil {
		return nil, assetImage{}, err
	}
	if err := validateAssetImage(raw, legacy[0].extension); err != nil {
		return nil, assetImage{}, fmt.Errorf("validating legacy avatar %q: %w", legacy[0].fileName, err)
	}
	if afterLegacyRead != nil {
		afterLegacyRead()
	}
	fileName, err := writeImageAtRoot(root, "avatars", base, legacy[0].extension, raw)
	if err != nil {
		return nil, assetImage{}, fmt.Errorf("migrating legacy avatar: %w", err)
	}
	if err := removeKnownVariants(root, "avatars", legacyFiles); err != nil {
		return nil, assetImage{}, fmt.Errorf("removing legacy avatar: %w", err)
	}
	return raw, assetImage{
		base: base, fileName: fileName, contentType: legacy[0].contentType, extension: legacy[0].extension,
	}, nil
}

func safeLegacyAvatarVariants(root *os.Root, uniqueID, hashedBase string) ([]assetImage, error) {
	if uniqueID == hashedBase || validateAssetComponent("legacy avatar name", uniqueID) != nil {
		return nil, nil
	}
	return existingImageVariants(root, "avatars", uniqueID)
}

func (s assetStorage) removeImage(dir, base string) (string, error) {
	if err := validateAssetComponent("asset name", base); err != nil {
		return "", err
	}
	unlock := s.lockAssets(dir, true, base)
	defer unlock()

	root, err := s.openRoot()
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()

	image, err := removeImagesAtRoot(root, dir, base, true)
	if err != nil {
		return "", err
	}
	return image.fileName, nil
}

func removeImagesAtRoot(root *os.Root, dir, base string, required bool) (assetImage, error) {
	variants, err := existingImageVariants(root, dir, base)
	if err != nil {
		return assetImage{}, err
	}
	if len(variants) == 0 {
		if required {
			return assetImage{}, fs.ErrNotExist
		}
		return assetImage{}, nil
	}
	if err := removeKnownVariants(root, dir, variants); err != nil {
		return assetImage{}, err
	}
	return variants[0], nil
}

func removeKnownVariants(root *os.Root, dir string, variants []assetImage) error {
	if len(variants) == 0 {
		return nil
	}
	for _, image := range variants {
		if err := root.Remove(filepath.Join(dir, image.fileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing asset variant %q: %w", image.fileName, err)
		}
	}
	return syncAssetDir(root, dir)
}

func (s assetStorage) renameImage(dir, oldBase, newBase string) (string, error) {
	if err := validateAssetComponent("old asset name", oldBase); err != nil {
		return "", err
	}
	if err := validateAssetComponent("new asset name", newBase); err != nil {
		return "", err
	}
	unlock := s.lockAssets(dir, true, oldBase, newBase)
	defer unlock()

	root, err := s.openRoot()
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()

	sources, err := existingImageVariants(root, dir, oldBase)
	if err != nil {
		return "", err
	}
	if len(sources) == 0 {
		return "", fs.ErrNotExist
	}
	validSources, err := validImageVariants(root, dir, oldBase)
	if err != nil {
		return "", err
	}
	if len(validSources) == 0 {
		return "", fs.ErrNotExist
	}
	destinations, err := existingImageVariants(root, dir, newBase)
	if err != nil {
		return "", err
	}
	if len(destinations) > 0 {
		return "", fs.ErrExist
	}
	chosen := validSources[0]
	oldPath := filepath.Join(dir, chosen.fileName)
	newPath, err := assetImagePath(dir, newBase, chosen.extension)
	if err != nil {
		return "", err
	}
	if err := root.Rename(oldPath, newPath); err != nil {
		return "", fmt.Errorf("renaming asset: %w", err)
	}
	otherSources := make([]assetImage, 0, len(sources)-1)
	for _, source := range sources {
		if source.fileName != chosen.fileName {
			otherSources = append(otherSources, source)
		}
	}
	if err := removeKnownVariants(root, dir, otherSources); err != nil {
		return "", err
	}
	if err := syncAssetDir(root, dir); err != nil {
		return "", err
	}
	return filepath.Base(newPath), nil
}

func (s assetStorage) listImages(dir string) ([]assetImage, error) {
	if err := validateAssetDir(dir); err != nil {
		return nil, err
	}
	unlock := s.lockNamespace(dir)
	defer unlock()

	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := normalizeAssetDir(root, dir); err != nil {
		return nil, err
	}
	entries, err := readAssetDir(root, dir)
	if err != nil {
		return nil, err
	}

	byBase := make(map[string]assetImage, len(entries))
	for _, entry := range entries {
		format, ok := assetFormatForExtension(filepath.Ext(entry.Name()))
		if !ok {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), format.extension)
		if err := validateAssetComponent("asset name", base); err != nil {
			continue
		}
		rel := filepath.Join(dir, entry.Name())
		if _, err := regularAssetInfo(root, rel); err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errUnsafeAsset) {
				continue
			}
			return nil, err
		}
		raw, err := readRegularAsset(root, rel, maxImageBytes)
		if err != nil || validateAssetImage(raw, format.extension) != nil {
			// Directory contents are untrusted. A malformed or oversized
			// preferred variant must not hide a valid lower-ranked one.
			continue
		}
		candidate := assetImage{
			base: base, fileName: entry.Name(), contentType: format.contentType, extension: format.extension,
		}
		current, exists := byBase[base]
		if !exists || assetFormatRank(candidate.extension) < assetFormatRank(current.extension) {
			byBase[base] = candidate
		}
	}
	images := make([]assetImage, 0, len(byBase))
	for _, image := range byBase {
		images = append(images, image)
	}
	sort.Slice(images, func(i, j int) bool { return images[i].fileName < images[j].fileName })
	return images, nil
}

func snapshotImageVariants(root *os.Root, dir, base string) ([]assetImageSnapshot, error) {
	variants, err := existingImageVariants(root, dir, base)
	if err != nil {
		return nil, err
	}
	snapshots := make([]assetImageSnapshot, 0, len(variants))
	for _, image := range variants {
		raw, err := readRegularAsset(root, filepath.Join(dir, image.fileName), maxImageBytes)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, assetImageSnapshot{image: image, data: raw})
	}
	return snapshots, nil
}

func restoreImageVariants(root *os.Root, dir, base string, snapshots []assetImageSnapshot) error {
	if _, err := removeImagesAtRoot(root, dir, base, false); err != nil {
		return err
	}
	if len(snapshots) == 0 {
		return nil
	}
	if err := ensureAssetDir(root, dir); err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		rel, err := assetImagePath(dir, base, snapshot.image.extension)
		if err != nil {
			return err
		}
		if err := writeAssetAtomic(root, rel, snapshot.data); err != nil {
			return err
		}
	}
	return syncAssetDir(root, dir)
}

func restoreImageVariantsAndVerify(
	root *os.Root,
	dir, base string,
	snapshots []assetImageSnapshot,
	restore func(*os.Root, string, string, []assetImageSnapshot) error,
) error {
	if restore == nil {
		return errors.New("asset restore operation is nil")
	}
	if err := restore(root, dir, base, snapshots); err != nil {
		return err
	}
	return verifyImageVariants(root, dir, base, snapshots)
}

func verifyImageVariants(root *os.Root, dir, base string, snapshots []assetImageSnapshot) error {
	expected := make(map[string][]byte, len(snapshots))
	for _, snapshot := range snapshots {
		rel, err := assetImagePath(dir, base, snapshot.image.extension)
		if err != nil || filepath.Base(rel) != snapshot.image.fileName {
			return errors.New("invalid asset rollback snapshot identity")
		}
		if _, duplicate := expected[snapshot.image.extension]; duplicate {
			return errors.New("duplicate asset rollback snapshot extension")
		}
		expected[snapshot.image.extension] = snapshot.data
	}

	actual, err := existingImageVariants(root, dir, base)
	if err != nil {
		return fmt.Errorf("enumerating restored asset variants: %w", err)
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("restored asset variant count is %d, want %d", len(actual), len(expected))
	}
	for _, image := range actual {
		want, ok := expected[image.extension]
		if !ok {
			return fmt.Errorf("restored asset has unexpected variant %q", image.fileName)
		}
		got, err := readRegularAsset(root, filepath.Join(dir, image.fileName), maxImageBytes)
		if err != nil {
			return fmt.Errorf("reading restored asset %q: %w", image.fileName, err)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("restored asset %q does not match its snapshot", image.fileName)
		}
	}
	return nil
}

// DiscoverChannelIconIDs returns canonical positive channel IDs backed by a
// supported regular image. Unsupported, malformed, directory, and symlink
// entries are ignored without being followed. Valid files and the icon
// directory are permission-normalized as they are discovered.
func DiscoverChannelIconIDs(rootDir string) ([]int64, error) {
	storage := assetStorage{rootDir: rootDir}
	unlock := storage.lockNamespace("icons")
	defer unlock()

	root, err := storage.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := normalizeAssetDir(root, "icons"); errors.Is(err, fs.ErrNotExist) {
		return []int64{}, nil
	} else if err != nil {
		return nil, err
	}
	entries, err := readAssetDir(root, "icons")
	if err != nil {
		return nil, err
	}

	ids := make(map[int64]struct{}, len(entries))
	for _, entry := range entries {
		format, ok := assetFormatForExtension(filepath.Ext(entry.Name()))
		if !ok {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), format.extension)
		id, err := strconv.ParseInt(base, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != base {
			continue
		}
		raw, err := readRegularAsset(root, filepath.Join("icons", entry.Name()), maxImageBytes)
		if err != nil {
			if errors.Is(err, errUnsafeAsset) || errors.Is(err, fs.ErrNotExist) {
				continue
			}
			// Invalid or oversized image files are untrusted startup input and
			// simply do not mark a channel as having a usable icon.
			continue
		}
		if err := validateAssetImage(raw, format.extension); err != nil {
			continue
		}
		ids[id] = struct{}{}
	}
	out := make([]int64, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func findAssetImage(root *os.Root, dir, base string) (assetImage, error) {
	variants, err := validImageVariants(root, dir, base)
	if err != nil {
		return assetImage{}, err
	}
	if len(variants) == 0 {
		return assetImage{}, fs.ErrNotExist
	}
	return variants[0], nil
}

func existingImageVariants(root *os.Root, dir, base string) ([]assetImage, error) {
	variants, err := imageVariants(root, dir, base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return variants, err
}

func validImageVariants(root *os.Root, dir, base string) ([]assetImage, error) {
	variants, err := existingImageVariants(root, dir, base)
	if err != nil {
		return nil, err
	}
	valid := make([]assetImage, 0, len(variants))
	for _, candidate := range variants {
		raw, err := readRegularAsset(root, filepath.Join(dir, candidate.fileName), maxImageBytes)
		if err != nil {
			// Oversized or concurrently removed regular candidates are unusable
			// but do not mask a later valid extension.
			continue
		}
		if err := validateAssetImage(raw, candidate.extension); err != nil {
			continue
		}
		valid = append(valid, candidate)
	}
	return valid, nil
}

func imageVariants(root *os.Root, dir, base string) ([]assetImage, error) {
	if err := validateAssetDir(dir); err != nil {
		return nil, err
	}
	if err := validateAssetComponent("asset name", base); err != nil {
		return nil, err
	}
	if err := normalizeAssetDir(root, dir); err != nil {
		return nil, err
	}
	variants := make([]assetImage, 0, len(assetImageFormats))
	for _, format := range assetImageFormats {
		rel, err := assetImagePath(dir, base, format.extension)
		if err != nil {
			return nil, err
		}
		if _, err := regularAssetInfo(root, rel); err == nil {
			variants = append(variants, assetImage{
				base: base, fileName: filepath.Base(rel), contentType: format.contentType, extension: format.extension,
			})
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return variants, nil
}

func assetImagePath(dir, base, extension string) (string, error) {
	if err := validateAssetDir(dir); err != nil {
		return "", err
	}
	if err := validateAssetComponent("asset name", base); err != nil {
		return "", err
	}
	if _, ok := assetFormatForExtension(extension); !ok {
		return "", fmt.Errorf("%w: unsupported asset extension %q", errUnsafeAsset, extension)
	}
	if dir == "." {
		dir = ""
	}
	return filepath.Join(dir, base+extension), nil
}

func validateAssetDir(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	return validateAssetComponent("asset directory", dir)
}

func validateAssetComponent(label, value string) error {
	if value == "" || value == "." || value == ".." ||
		strings.ContainsAny(value, `/\\`) || filepath.Base(value) != value || !filepath.IsLocal(value) {
		return fmt.Errorf("%w: invalid %s %q", errUnsafeAsset, label, value)
	}
	return nil
}

func assetFormatForExtension(extension string) (assetImageFormat, bool) {
	for _, format := range assetImageFormats {
		if format.extension == extension {
			return format, true
		}
	}
	return assetImageFormat{}, false
}

func assetFormatForDecoder(decoderName string) (assetImageFormat, bool) {
	for _, format := range assetImageFormats {
		if format.decoderName == decoderName {
			return format, true
		}
	}
	return assetImageFormat{}, false
}

// inspectAssetImage accepts only bounded, static images. Animation is rejected
// from the PNG/GIF/WebP container before a decoder can allocate frame buffers, and
// a process-wide semaphore bounds parallel full-image allocations from served
// reads and directory scans.
func inspectAssetImage(raw []byte) (assetImageFormat, error) {
	return inspectAssetImageWithSlots(raw, assetImageDecodeSlots)
}

func inspectAssetImageWithSlots(raw []byte, slots chan struct{}) (assetImageFormat, error) {
	if len(raw) == 0 {
		return assetImageFormat{}, errors.New("empty image data")
	}
	if len(raw) > maxImageBytes {
		return assetImageFormat{}, fmt.Errorf("image exceeds %d bytes", maxImageBytes)
	}
	if err := validateStaticImageContainer(raw); err != nil {
		return assetImageFormat{}, err
	}
	return withAssetImageDecodeSlot(slots, func() (assetImageFormat, error) {
		config, decoderName, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			return assetImageFormat{}, fmt.Errorf("decoding image configuration: %w", err)
		}
		format, ok := assetFormatForDecoder(decoderName)
		if !ok {
			return assetImageFormat{}, fmt.Errorf("unsupported decoded image type %q", decoderName)
		}
		if config.Width <= 0 || config.Height <= 0 ||
			config.Width > maxImageDimension || config.Height > maxImageDimension ||
			int64(config.Width)*int64(config.Height) > maxImagePixels {
			return assetImageFormat{}, fmt.Errorf("image dimensions %dx%d exceed the limit", config.Width, config.Height)
		}
		decoded, decodedName, err := image.Decode(bytes.NewReader(raw))
		if err != nil {
			return assetImageFormat{}, fmt.Errorf("decoding image: %w", err)
		}
		if decoded == nil || decodedName != decoderName {
			return assetImageFormat{}, errors.New("decoded image type changed between validation passes")
		}
		return format, nil
	})
}

func withAssetImageDecodeSlot(
	slots chan struct{},
	decode func() (assetImageFormat, error),
) (assetImageFormat, error) {
	if slots == nil || decode == nil {
		return assetImageFormat{}, errors.New("image decode operation is nil")
	}
	slots <- struct{}{}
	defer func() { <-slots }()
	return decode()
}

func validateStaticImageContainer(raw []byte) error {
	if bytes.HasPrefix(raw, staticPNGSignature) {
		return validateStaticPNG(raw)
	}
	if bytes.HasPrefix(raw, []byte("GIF87a")) || bytes.HasPrefix(raw, []byte("GIF89a")) {
		return validateStaticGIF(raw)
	}
	if len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP" {
		return validateStaticWebP(raw)
	}
	return nil
}

func validateStaticPNG(raw []byte) error {
	if len(raw) < len(staticPNGSignature) || !bytes.Equal(raw[:len(staticPNGSignature)], staticPNGSignature) {
		return errors.New("malformed PNG signature")
	}
	position := len(staticPNGSignature)
	for position < len(raw) {
		if len(raw)-position < 12 {
			return errors.New("malformed PNG chunk")
		}
		dataLength, err := safecast.Uint32ToInt(binary.BigEndian.Uint32(raw[position : position+4]))
		if err != nil || dataLength > len(raw)-position-12 {
			return errors.New("malformed PNG chunk length")
		}
		chunkEnd := position + 12 + dataLength
		chunkType := string(raw[position+4 : position+8])
		switch chunkType {
		case "acTL", "fcTL", "fdAT":
			return errors.New("animated PNG images are not supported")
		}
		position = chunkEnd
		if chunkType == "IEND" {
			if dataLength != 0 || position != len(raw) {
				return errors.New("malformed PNG IEND chunk")
			}
			return nil
		}
	}
	return errors.New("malformed PNG container without IEND")
}

func validateStaticGIF(raw []byte) error {
	if len(raw) < 13 {
		return errors.New("malformed GIF container")
	}
	position := 13
	if raw[10]&0x80 != 0 {
		colorTableBytes := 3 * (1 << ((raw[10] & 0x07) + 1))
		if colorTableBytes > len(raw)-position {
			return errors.New("malformed GIF global color table")
		}
		position += colorTableBytes
	}
	frames := 0
	for position < len(raw) {
		switch raw[position] {
		case 0x3b: // trailer
			if position != len(raw)-1 {
				return errors.New("malformed GIF trailing data")
			}
			return nil
		case 0x2c: // image descriptor
			frames++
			if frames > 1 {
				return errors.New("animated GIF images are not supported")
			}
			if len(raw)-position < 10 {
				return errors.New("malformed GIF image descriptor")
			}
			packed := raw[position+9]
			position += 10
			if packed&0x80 != 0 {
				colorTableBytes := 3 * (1 << ((packed & 0x07) + 1))
				if colorTableBytes > len(raw)-position {
					return errors.New("malformed GIF local color table")
				}
				position += colorTableBytes
			}
			if position >= len(raw) {
				return errors.New("malformed GIF image data")
			}
			position++ // LZW minimum code size
			var err error
			position, err = skipGIFSubBlocks(raw, position)
			if err != nil {
				return err
			}
		case 0x21: // extension
			if len(raw)-position < 2 {
				return errors.New("malformed GIF extension")
			}
			position += 2 // introducer and extension label
			var err error
			position, err = skipGIFSubBlocks(raw, position)
			if err != nil {
				return err
			}
		default:
			return errors.New("malformed GIF block")
		}
	}
	return errors.New("malformed GIF without trailer")
}

func skipGIFSubBlocks(raw []byte, position int) (int, error) {
	for position < len(raw) {
		size := int(raw[position])
		position++
		if size == 0 {
			return position, nil
		}
		if size > len(raw)-position {
			return 0, errors.New("malformed GIF sub-block")
		}
		position += size
	}
	return 0, errors.New("unterminated GIF sub-block")
}

func validateStaticWebP(raw []byte) error {
	if len(raw) < 12 {
		return errors.New("malformed WebP container")
	}
	declaredSize := int64(binary.LittleEndian.Uint32(raw[4:8])) + 8
	if declaredSize != int64(len(raw)) {
		return errors.New("malformed WebP RIFF length")
	}
	position := int64(12)
	for position < declaredSize {
		if declaredSize-position < 8 {
			return errors.New("malformed WebP chunk header")
		}
		start := int(position)
		chunkName := string(raw[start : start+4])
		chunkSize := int64(binary.LittleEndian.Uint32(raw[start+4 : start+8]))
		dataStart := position + 8
		dataEnd := dataStart + chunkSize
		if dataEnd < dataStart || dataEnd > declaredSize {
			return errors.New("malformed WebP chunk length")
		}
		if chunkName == "ANIM" || chunkName == "ANMF" {
			return errors.New("animated WebP images are not supported")
		}
		if chunkName == "VP8X" {
			if chunkSize < 1 {
				return errors.New("malformed WebP extended header")
			}
			if raw[int(dataStart)]&0x02 != 0 {
				return errors.New("animated WebP images are not supported")
			}
		}
		position = dataEnd + chunkSize%2
		if position > declaredSize {
			return errors.New("malformed WebP chunk padding")
		}
	}
	return nil
}

func validateAssetImage(raw []byte, expectedExtension string) error {
	expected, ok := assetFormatForExtension(expectedExtension)
	if !ok {
		return fmt.Errorf("unsupported image extension %q", expectedExtension)
	}
	actual, err := inspectAssetImage(raw)
	if err != nil {
		return err
	}
	if actual.extension != expected.extension {
		return fmt.Errorf("decoded %s image does not match %s extension", actual.decoderName, expected.extension)
	}
	return nil
}

func assetFormatRank(extension string) int {
	for index, format := range assetImageFormats {
		if format.extension == extension {
			return index
		}
	}
	return len(assetImageFormats)
}

func ensureAssetDir(root *os.Root, dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	if err := validateAssetDir(dir); err != nil {
		return err
	}
	if err := normalizeAssetDir(root, dir); errors.Is(err, fs.ErrNotExist) {
		if err := root.MkdirAll(dir, assetDirMode); err != nil {
			return fmt.Errorf("creating asset directory: %w", err)
		}
	} else if err != nil {
		return err
	} else {
		return nil
	}
	return normalizeAssetDir(root, dir)
}

func normalizeAssetDir(root *os.Root, dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	if err := validateAssetDir(dir); err != nil {
		return err
	}
	before, err := root.Lstat(dir)
	if err != nil {
		return err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return fmt.Errorf("%w: asset directory %q is not a real directory", errUnsafeAsset, dir)
	}
	directory, err := root.Open(dir)
	if err != nil {
		return err
	}
	opened, err := directory.Stat()
	if err != nil {
		return errors.Join(err, directory.Close())
	}
	if !opened.IsDir() || !os.SameFile(before, opened) {
		return errors.Join(
			fmt.Errorf("%w: asset directory %q changed while opening", errUnsafeAsset, dir),
			directory.Close(),
		)
	}
	if err := directory.Chmod(assetDirMode); err != nil {
		return errors.Join(fmt.Errorf("restricting asset directory permissions: %w", err), directory.Close())
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("closing asset directory after permission normalization: %w", err)
	}
	return nil
}

func readAssetDir(root *os.Root, dir string) ([]fs.DirEntry, error) {
	directory, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	return entries, nil
}

func regularAssetLstat(root *os.Root, rel string) (fs.FileInfo, error) {
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: asset %q is not a regular file", errUnsafeAsset, rel)
	}
	return info, nil
}

func openVerifiedRegularAsset(
	root *os.Root,
	rel string,
	expected fs.FileInfo,
	opener assetFileOpener,
) (*os.File, fs.FileInfo, error) {
	file, err := opener(rel)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		return nil, nil, errors.Join(err, file.Close())
	}
	if !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return nil, nil, errors.Join(
			fmt.Errorf("%w: asset %q changed while opening", errUnsafeAsset, rel),
			file.Close(),
		)
	}
	if err := file.Chmod(assetFileMode); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("restricting asset permissions: %w", err), file.Close())
	}
	return file, opened, nil
}

func regularAssetInfo(root *os.Root, rel string) (fs.FileInfo, error) {
	before, err := regularAssetLstat(root, rel)
	if err != nil {
		return nil, err
	}
	file, opened, err := openVerifiedRegularAsset(root, rel, before, root.Open)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("closing asset after permission normalization: %w", err)
	}
	return opened, nil
}

func readRegularAsset(root *os.Root, rel string, maxBytes int64) ([]byte, error) {
	return readRegularAssetWithOpener(root, rel, maxBytes, root.Open)
}

func readRegularAssetWithOpener(
	root *os.Root,
	rel string,
	maxBytes int64,
	opener assetFileOpener,
) (raw []byte, retErr error) {
	before, err := regularAssetLstat(root, rel)
	if err != nil {
		return nil, err
	}
	if before.Size() > maxBytes {
		return nil, fmt.Errorf("asset %q exceeds %d bytes", rel, maxBytes)
	}
	file, opened, err := openVerifiedRegularAsset(root, rel, before, opener)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("closing asset: %w", err))
		}
	}()
	if opened.Size() > maxBytes {
		return nil, fmt.Errorf("asset %q exceeds %d bytes", rel, maxBytes)
	}
	raw, err = io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("asset %q exceeds %d bytes", rel, maxBytes)
	}
	return raw, nil
}

func writeAssetAtomic(root *os.Root, rel string, data []byte) error {
	return writeAssetAtomicWithSync(root, rel, data, syncAssetDir)
}

func writeAssetAtomicWithSync(
	root *os.Root,
	rel string,
	data []byte,
	syncDir func(*os.Root, string) error,
) error {
	if syncDir == nil {
		return errors.New("asset directory sync operation is nil")
	}
	parent := filepath.Dir(rel)
	if parent == "" {
		parent = "."
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("generating asset temporary name: %w", err)
	}
	tempName := "." + filepath.Base(rel) + "." + hex.EncodeToString(random[:]) + ".tmp"
	tempPath := filepath.Join(parent, tempName)
	temp, err := root.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, assetFileMode)
	if err != nil {
		return fmt.Errorf("creating asset temporary file: %w", err)
	}
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = root.Remove(tempPath)
		}
	}()
	written, err := temp.Write(data)
	if err != nil {
		_ = temp.Close()
		return fmt.Errorf("writing asset: %w", err)
	}
	if written != len(data) {
		_ = temp.Close()
		return fmt.Errorf("writing asset: %w", io.ErrShortWrite)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("syncing asset: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("closing asset: %w", err)
	}
	if err := root.Rename(tempPath, rel); err != nil {
		return fmt.Errorf("installing asset: %w", err)
	}
	keepTemp = false
	return syncDir(root, parent)
}

func syncAssetDir(root *os.Root, dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if dir == "" {
		dir = "."
	}
	directory, err := root.Open(dir)
	if err != nil {
		return fmt.Errorf("opening asset directory for sync: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return fmt.Errorf("syncing asset directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("closing asset directory: %w", err)
	}
	return nil
}
