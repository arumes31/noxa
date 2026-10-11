package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClientUpdateReservationCleanupRequiresOriginalIdentity(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned reservation", true: "external empty replacement"}[changed], func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), ".previous-*.exe")
			if err != nil {
				t.Fatal(err)
			}
			path := file.Name()
			identity, statErr := file.Stat()
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if statErr != nil {
				t.Fatal(statErr)
			}
			if changed {
				if err := os.Rename(path, filepath.Join(filepath.Dir(path), "original-reservation")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			removeClientUpdateReservation(path, identity)
			_, err = os.Lstat(path)
			if changed && err != nil {
				t.Fatalf("external empty file removed: %v", err)
			}
			if !changed && !os.IsNotExist(err) {
				t.Fatalf("owned reservation not removed: %v", err)
			}
		})
	}
}
