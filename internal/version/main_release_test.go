package version

import (
	"path/filepath"
	"testing"
)

func TestMainReleaseTags(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "VERSION"), "0.4.0\n")
	runTestGit(t, root, "init")
	runTestGit(t, root, "config", "user.name", "Version Test")
	runTestGit(t, root, "config", "user.email", "version-test@example.invalid")
	runTestGit(t, root, "add", ".")
	runTestGit(t, root, "commit", "-m", "initial")
	for _, tc := range []struct {
		tag, want string
	}{
		{"v0.4.0-main.9", "0.4.0-main.9"},
		{"v0.4.0-main.10", "0.4.0-main.10"},
		{"v0.4.0", "0.4.0"},
	} {
		runTestGit(t, root, "tag", tc.tag)
		metadata, err := Detect(root)
		if err != nil {
			t.Fatal(err)
		}
		if metadata.Dirty || metadata.Version != tc.want {
			t.Fatalf("after tagging %s: got %+v, want %s", tc.tag, metadata, tc.want)
		}
	}
	runTestGit(t, root, "tag", "v0.4.0-rc.1")
	if _, err := Detect(root); err == nil {
		t.Fatal("automatic tags must not hide ambiguous intentional release tags")
	}
}
