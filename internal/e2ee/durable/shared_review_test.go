package durable

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSharedMissingDirectoryThroughLink(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	link := filepath.Join(root, "link")
	if e := os.Mkdir(actual, 0700); e != nil {
		t.Fatal(e)
	}
	if runtime.GOOS == "windows" {
		if b, e := exec.Command("cmd", "/c", "mklink", "/J", link, actual).CombinedOutput(); e != nil {
			t.Fatalf("junction: %s %v", b, e)
		}
	} else if e := os.Symlink(actual, link); e != nil {
		t.Fatal(e)
	}
	p := IdentityProtection(bytes.Repeat([]byte{7}, 32))
	a, e := OpenShared(filepath.Join(link, "new"), testNS, p)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := OpenShared(filepath.Join(actual, "new"), testNS, p)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if a.Store != b.Store {
		t.Fatal("distinct shared handles")
	}
}
