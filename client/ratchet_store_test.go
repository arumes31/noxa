package main

import (
	"bytes"
	"noxa/internal/e2ee/durable"
	"os"
	"testing"
)

func TestDurableRatchetPlatformProtection(t *testing.T) {
	dir := t.TempDir()
	ns := durable.Namespace{Server: "authenticated-server", Principal: "alice", Device: "device"}
	secret := bytes.Repeat([]byte{7}, 32)
	s, err := openDurableRatchets(dir, ns, secret)
	if err != nil {
		t.Fatal(err)
	}
	key := s.CoreKey()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openDurableRatchets(dir, ns, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if !bytes.Equal(key, s.CoreKey()) {
		t.Fatal("platform protection changed master key")
	}
	notice, err := os.ReadFile("frontend/public/ratchet-storage-licenses.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(notice, []byte(durable.BBoltLicense)) {
		t.Fatal("desktop packaged bbolt notice differs")
	}
}
