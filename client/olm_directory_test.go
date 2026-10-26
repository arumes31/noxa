package main

import (
	"encoding/json"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/netproto"
	"os"
	"testing"
	"time"
)

func TestNativeOlmDirectoryConsumer(t *testing.T) {
	var f struct {
		Server         string
		Principal      string
		DeviceID       string `json:"device_id"`
		RootSigning    []byte `json:"root_signing"`
		RootEncryption []byte `json:"root_encryption"`
		Devices        []netproto.DMDevice
		Bundle         olmdir.Bundle
		KeyID          string `json:"key_id"`
		Now            int64
	}
	data, e := os.ReadFile("../internal/e2ee/olmdir/testdata/directory-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(data, &f); e != nil {
		t.Fatal(e)
	}
	var root [32]byte
	copy(root[:], f.RootEncryption)
	key, e := verifyOlmClaim(f.Bundle, f.Server, f.Principal, f.DeviceID, f.Bundle.Identity, f.KeyID, f.RootSigning, root, f.Devices, time.Unix(f.Now, 0))
	if e != nil || key.ID != f.KeyID {
		t.Fatal("native consumer", e)
	}
	f.Devices[0].RevokedAt = f.Now
	if _, e = verifyOlmClaim(f.Bundle, f.Server, f.Principal, f.DeviceID, f.Bundle.Identity, f.KeyID, f.RootSigning, root, f.Devices, time.Unix(f.Now, 0)); e == nil {
		t.Fatal("revoked root trusted")
	}
}
