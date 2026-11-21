// Package speechnames voices session names for the desktop client's
// announcements ("Joshi ist dem Channel beigetreten"). Each (language,
// normalized name) is synthesized once with the ST13 voice, stored as a WAV
// under the data directory and pruned after 30 days without use.
package speechnames

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"go.uber.org/zap"
)

const (
	MaxNameRunes     = 32
	synthesisTimeout = 15 * time.Second
	// Unused clips are deleted after this; a later fetch regenerates them.
	Retention = 30 * 24 * time.Hour
	// A name that failed to synthesize is not tried again for this long.
	failureRetry = time.Hour
	maxFailures  = 1024
)

// Languages lists the recorded announcement languages.
var Languages = []string{"en", "de"}

var (
	ErrUnavailable = errors.New("speechnames: unavailable")
	ErrUnspeakable = errors.New("speechnames: nothing speakable in this name")
	ErrRateLimited = errors.New("speechnames: synthesis budget exhausted")
	ErrPending     = errors.New("speechnames: clip is being synthesized")
	dedupeSuffix   = regexp.MustCompile(`#M?[0-9]+$`)
)

// Normalize returns the spoken form of a nickname: without the server's
// duplicate suffix (#2, #M1), emoji, control and format characters, at most
// MaxNameRunes runes. It returns "" when nothing speakable is left.
func Normalize(nickname string) string {
	name := dedupeSuffix.ReplaceAllString(strings.TrimSpace(nickname), "")
	name = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case r == unicode.ReplacementChar, unicode.In(r, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs, unicode.So, unicode.Sk, unicode.Me),
			unicode.Is(unicode.Variation_Selector, r):
			return -1
		}
		return r
	}, name)
	runes := []rune(strings.Join(strings.Fields(name), " "))
	name = strings.TrimSpace(string(runes[:min(len(runes), MaxNameRunes)]))
	if strings.IndexFunc(name, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0 {
		return ""
	}
	return name
}

// Key identifies the clip of a normalized name in one language.
func Key(language, name string) string {
	sum := sha256.Sum256([]byte(language + "\n" + name))
	return hex.EncodeToString(sum[:])
}

// Synthesizer renders text as 24 kHz mono samples.
type Synthesizer interface {
	Synthesize(ctx context.Context, language, text string) ([]float64, error)
}

// Service generates, caches and prunes name clips. A nil *Service is disabled.
type Service struct {
	dir      string
	synth    Synthesizer
	logger   *zap.Logger
	slots    chan struct{} // concurrent syntheses
	mu       sync.Mutex
	inflight map[string]struct{}
	failed   map[string]time.Time // key -> failure time, bounded by maxFailures
	disabled atomic.Bool
}

// New stores clips under dir. It never fails: a broken engine turns the
// feature off later.
func New(dir string, synth Synthesizer, logger *zap.Logger) *Service {
	return &Service{dir: dir, synth: synth, logger: logger, slots: make(chan struct{}, 1), inflight: map[string]struct{}{}, failed: map[string]time.Time{}}
}

// Open returns the service backed by the image's engine, or nil (disabled,
// logged once) when the engine or its model is missing.
func Open(dir string, logger *zap.Logger) *Service {
	engine, err := NewEngine(DefaultTTS, DefaultModelDir, dir)
	if err != nil {
		logger.Info("spoken names disabled: speech engine unavailable", zap.Error(err))
		return nil
	}
	return New(dir, engine, logger)
}

// Enabled reports whether clips can still be produced.
func (s *Service) Enabled() bool { return s != nil && !s.disabled.Load() }

// PronunciationsFile, in the clip directory, lists names the voice reads
// wrongly with the spelling it should read instead, per language:
// {"de": {"Dave": "Däiv"}, "en": {"Joshibär": "Yoshi bear"}}.
const PronunciationsFile = "pronunciations.json"

// spoken returns the text the engine reads for a normalized name and the
// clip's key. A listed spelling is part of the key, so editing the list
// makes a new clip; a name that is not listed keeps the key it always had.
func (s *Service) spoken(language, name string) (text, key string) {
	// shortcut: the list is read on every request, like the clip itself; cache it if it grows past a few hundred names.
	data, err := os.ReadFile(filepath.Join(s.dir, PronunciationsFile)) // #nosec G304 -- fixed file in the clip directory
	if err != nil {
		return name, Key(language, name)
	}
	var list map[string]map[string]string
	if err := json.Unmarshal(data, &list); err != nil {
		s.logger.Debug("spoken names: pronunciation list ignored", zap.Error(err))
		return name, Key(language, name)
	}
	// Entries that differ only in case all match: the first in sorted order
	// wins, so every request picks the same one.
	first, text := "", ""
	for written, as := range list[language] {
		if as = Normalize(as); as != "" && strings.EqualFold(Normalize(written), name) && (text == "" || written < first) {
			first, text = written, as
		}
	}
	if text == "" || text == name {
		return name, Key(language, name)
	}
	return text, Key(language, name+"\n"+text)
}

func (s *Service) path(language, key string) string {
	return filepath.Join(s.dir, language, key+".wav")
}

// Clip returns the stored WAV for nickname. A clip not stored yet is
// synthesized in the background and Clip returns ErrPending at once: a
// request never waits for synthesis. allowSynthesis (nil: always) is asked
// only when a new synthesis would start.
func (s *Service) Clip(language, nickname string, allowSynthesis func() bool) (key string, wav []byte, err error) {
	if !s.Enabled() {
		return "", nil, ErrUnavailable
	}
	name := Normalize(nickname)
	if name == "" {
		return "", nil, ErrUnspeakable
	}
	text, key := s.spoken(language, name)
	path := s.path(language, key)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Under s.mu: Prune cannot delete the clip between this read and its touch,
	// and a synthesis that just finished is seen before a new one is charged.
	if data, err := os.ReadFile(path); err == nil { // #nosec G304 -- path is the clip dir plus a SHA-256 name
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return key, data, nil
	}
	if _, running := s.inflight[key]; running {
		return key, nil, ErrPending
	}
	if at, failed := s.failed[key]; failed && time.Since(at) < failureRetry {
		return key, nil, ErrUnavailable
	}
	if allowSynthesis != nil && !allowSynthesis() {
		return "", nil, ErrRateLimited
	}
	s.inflight[key] = struct{}{}
	go func() {
		err := s.generate(language, text, path)
		if err != nil && !errors.Is(err, ErrUnavailable) {
			s.logger.Debug("spoken name not synthesized", zap.Error(err))
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.inflight, key)
		// A name the engine or mastering rejects would fail the same way on
		// every request; a timeout may only mean load, so it is retried.
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			s.rememberFailure(key, time.Now())
		}
	}()
	return key, nil, ErrPending
}

func (s *Service) generate(language, name, path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), synthesisTimeout)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	samples, err := s.synth.Synthesize(ctx, language, name)
	if errors.Is(err, ErrEngine) {
		// One odd name must not switch the feature off for everyone: only an
		// engine that also fails a known word is broken.
		probe, cancelProbe := context.WithTimeout(context.Background(), synthesisTimeout)
		defer cancelProbe()
		if _, probeErr := s.synth.Synthesize(probe, "en", "Test"); errors.Is(probeErr, ErrEngine) && !s.disabled.Swap(true) {
			s.logger.Warn("spoken names disabled: speech engine failed", zap.Error(probeErr))
		}
		return ErrUnavailable
	}
	if err != nil {
		return err
	}
	data, err := master(samples)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "clip-*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// rememberFailure records a failed clip; the caller holds s.mu.
func (s *Service) rememberFailure(key string, now time.Time) {
	if len(s.failed) >= maxFailures {
		for k, at := range s.failed {
			if now.Sub(at) >= failureRetry {
				delete(s.failed, k)
			}
		}
	}
	for k := range s.failed { // still full: drop an arbitrary entry
		if len(s.failed) < maxFailures {
			break
		}
		delete(s.failed, k)
	}
	s.failed[key] = now
}

// Touch marks the clips of nickname as used (a session with that name
// connected or took it), so retention keeps them.
func (s *Service) Touch(nickname string) {
	name := Normalize(nickname)
	if s == nil || name == "" {
		return
	}
	now := time.Now()
	for _, language := range Languages {
		_, key := s.spoken(language, name)
		s.mu.Lock()
		_ = os.Chtimes(s.path(language, key), now, now)
		s.mu.Unlock()
	}
}

// Prune deletes clips (and leftovers of interrupted writes) unused for Retention.
// It works inside an os.Root, so a swapped symlink cannot lead it outside dir.
func (s *Service) Prune(ctx context.Context, now time.Time) error {
	if s == nil {
		return nil
	}
	root, err := os.OpenRoot(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if ext := filepath.Ext(path); ext != ".wav" && ext != ".tmp" {
			return nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		// Fresh stat under s.mu: a clip used since the walk listed it stays.
		if info, err := root.Stat(path); err == nil && now.Sub(info.ModTime()) > Retention {
			return root.Remove(path)
		}
		return nil
	})
}
