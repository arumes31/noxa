package filetransfer

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Issued tokens that are never presented must not stay resident after their
// TTL, and the registry must refuse issuance once it is full.
func TestRegisterPrunesExpiredAndCapsPendingTransfers(t *testing.T) {
	s := New(Config{Addr: ":0", RootDir: t.TempDir()}, newFakeFileStore(), nil)
	ctx := context.Background()

	old := tokenTTL
	tokenTTL = 10 * time.Millisecond
	defer func() { tokenTTL = old }()
	for range 5 {
		if _, _, err := s.InitUpload(ctx, 7, "", "a.txt", 10, "uid-1", 0); err != nil {
			t.Fatalf("InitUpload: %v", err)
		}
	}
	time.Sleep(30 * time.Millisecond)
	if _, _, err := s.InitUpload(ctx, 7, "", "b.txt", 10, "uid-1", 0); err != nil {
		t.Fatalf("InitUpload after expiry: %v", err)
	}
	if got := s.pendingCount(); got != 1 {
		t.Fatalf("pending = %d, want 1 (expired tokens pruned)", got)
	}

	tokenTTL = time.Hour
	s.mu.Lock()
	// The short-TTL token issued above would be pruned by the at-capacity
	// sweep if filling took longer than its TTL; keep it live.
	for _, tr := range s.transfers {
		tr.Expires = time.Now().Add(time.Hour)
	}
	for i := len(s.transfers); i < maxPendingTransfers; i++ {
		s.transfers["filler-"+strconv.Itoa(i)] = &transfer{Expires: time.Now().Add(time.Hour)}
	}
	s.mu.Unlock()
	if _, _, err := s.InitUpload(ctx, 7, "", "c.txt", 10, "uid-1", 0); !errors.Is(err, ErrTooManyPendingTransfers) {
		t.Fatalf("InitUpload on full registry err=%v, want ErrTooManyPendingTransfers", err)
	}
}

func TestSanitizeFolderBoundsLength(t *testing.T) {
	segment := strings.Repeat("a", 200)
	long := segment
	for len(long) <= maxFolderLen {
		long += "/" + segment
	}
	if _, err := sanitizeFolder(long); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("over-long folder err=%v, want ErrInvalidName", err)
	}
	if _, err := sanitizeFolder(segment + "/" + segment); err != nil {
		t.Fatalf("normal folder rejected: %v", err)
	}
}
