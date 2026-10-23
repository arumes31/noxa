package netproto

import "testing"

func TestPortableFileLeaf(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../file", `a\b`, "file:stream", "NUL.txt", "COM1", "LPT².log", "trailing.", "trailing ", "bad\x00name", "bad\u202ename"} {
		if ValidFileLeaf(name) {
			t.Errorf("accepted %q", name)
		}
	}
	for _, name := range []string{"report.pdf", "résumé.txt", "日本語.txt", "👩‍💻.png"} {
		if !ValidFileLeaf(name) {
			t.Errorf("rejected %q", name)
		}
	}
}
