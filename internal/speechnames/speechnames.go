// Package speechnames voices session names for the desktop client's
// announcements ("Joshi ist dem Channel beigetreten"). Each (language,
// normalized name) is synthesized once with the ST13 voice, stored as a WAV
// under the data directory and pruned after 30 days without use.
package speechnames

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
)

// Languages lists the recorded announcement languages.
var Languages = []string{"en", "de"}

var (
	ErrUnavailable = errors.New("speechnames: unavailable")
	ErrUnspeakable = errors.New("speechnames: nothing speakable in this name")
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

type call struct {
	done chan struct{}
	data []byte
	err  error
}

// Service generates, caches and prunes name clips. A nil *Service is disabled.
type Service struct {
	dir      string
	synth    Synthesizer
	logger   *zap.Logger
	slots    chan struct{} // concurrent syntheses
	mu       sync.Mutex
	inflight map[string]*call
	disabled atomic.Bool
}

// New stores clips under dir. It never fails: a broken engine turns the
// feature off later.
func New(dir string, synth Synthesizer, logger *zap.Logger) *Service {
	return &Service{dir: dir, synth: synth, logger: logger, slots: make(chan struct{}, 1), inflight: map[string]*call{}}
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

func (s *Service) path(language, name string) string {
	return filepath.Join(s.dir, language, Key(language, name)+".wav")
}

// Clip returns the WAV for nickname, synthesizing it on first use. Concurrent
// requests for one clip share a single synthesis.
func (s *Service) Clip(ctx context.Context, language, nickname string) (key string, wav []byte, err error) {
	if !s.Enabled() {
		return "", nil, ErrUnavailable
	}
	name := Normalize(nickname)
	if name == "" {
		return "", nil, ErrUnspeakable
	}
	key, path := Key(language, name), s.path(language, name)
	if data, err := os.ReadFile(path); err == nil {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return key, data, nil
	}
	s.mu.Lock()
	c, ok := s.inflight[key]
	if !ok {
		c = &call{done: make(chan struct{})}
		s.inflight[key] = c
		// The synthesis outlives the first requester: others may wait for it.
		go func() {
			c.data, c.err = s.generate(language, name, path)
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
			close(c.done)
		}()
	}
	s.mu.Unlock()
	select {
	case <-c.done:
		return key, c.data, c.err
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
}

func (s *Service) generate(language, name, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), synthesisTimeout)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	samples, err := s.synth.Synthesize(ctx, language, name)
	if errors.Is(err, ErrEngine) {
		if !s.disabled.Swap(true) {
			s.logger.Warn("spoken names disabled: speech engine failed", zap.Error(err))
		}
		return nil, ErrUnavailable
	}
	if err != nil {
		return nil, err
	}
	data, err := master(samples)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "clip-*.tmp")
	if err != nil {
		return nil, err
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
		return nil, err
	}
	return data, nil
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
		_ = os.Chtimes(s.path(language, name), now, now)
	}
}

// Prune deletes clips (and leftovers of interrupted writes) unused for Retention.
func (s *Service) Prune(ctx context.Context, now time.Time) error {
	if s == nil {
		return nil
	}
	err := filepath.WalkDir(s.dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ext := filepath.Ext(path)
		if ext != ".wav" && ext != ".tmp" {
			return nil
		}
		if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > Retention {
			return os.Remove(path)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
