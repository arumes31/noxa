package updatepatch

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

func testFiles() (oldFile, newFile []byte) {
	rng := rand.New(rand.NewPCG(1, 2))
	oldFile = make([]byte, 3<<20)
	for i := range oldFile {
		oldFile[i] = byte(rng.Uint32())
	}
	newFile = append([]byte(nil), oldFile...)
	copy(newFile[1<<20:], "a small change in the middle")
	newFile = append(newFile, "appended tail"...)
	return oldFile, newFile
}

func TestRoundTripIsSmallAndExact(t *testing.T) {
	oldFile, newFile := testFiles()
	patch, err := Create(oldFile, newFile)
	if err != nil {
		t.Fatal(err)
	}
	// Random data is incompressible: only the dictionary makes this small.
	if len(patch) > 64<<10 {
		t.Fatalf("patch is %d bytes; dictionary not used", len(patch))
	}
	got, err := Apply(oldFile, bytes.NewReader(patch), int64(len(newFile)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newFile) {
		t.Fatal("patched output differs")
	}
	again, err := Create(oldFile, newFile)
	if err != nil || !bytes.Equal(again, patch) {
		t.Fatal("patch creation is not deterministic")
	}
}

func TestApplyRejectsBadInput(t *testing.T) {
	oldFile, newFile := testFiles()
	patch, err := Create(oldFile, newFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(oldFile, bytes.NewReader(patch), int64(len(newFile))-1); err == nil {
		t.Fatal("accepted output over the size limit")
	}
	wrongBase := append([]byte(nil), oldFile...)
	wrongBase[0] ^= 0xff
	if got, err := Apply(wrongBase, bytes.NewReader(patch), int64(len(newFile))); err == nil && bytes.Equal(got, newFile) {
		t.Fatal("wrong base reproduced the new file")
	}
	corrupt := append([]byte(nil), patch...)
	corrupt[len(corrupt)/2] ^= 0xff
	if _, err := Apply(oldFile, bytes.NewReader(corrupt), int64(len(newFile))); err == nil {
		t.Fatal("accepted corrupt patch (frame CRC should fail)")
	}
}
