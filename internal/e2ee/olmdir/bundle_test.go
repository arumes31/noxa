package olmdir_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"noxa/internal/devicetrust"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/netproto"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSharedDirectoryVectors(t *testing.T) {
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
		Transcript     []byte
		ChainCases     []struct {
			Name    string
			Devices []netproto.DMDevice
			IDs     []string
		} `json:"chain_cases"`
		BundleCases []struct {
			Name   string
			Bundle olmdir.Bundle
		} `json:"bundle_cases"`
	}
	data, e := os.ReadFile("testdata/directory-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(data, &f); e != nil {
		t.Fatal(e)
	}
	transcript, e := f.Bundle.Transcript()
	if e != nil || !bytes.Equal(transcript, f.Transcript) {
		t.Fatal("cross-runtime transcript mismatch", e)
	}
	var rootX [32]byte
	copy(rootX[:], f.RootEncryption)
	for _, c := range f.ChainCases {
		t.Run(c.Name, func(t *testing.T) {
			ds, _ := devicetrust.Verify(f.Principal, f.RootSigning, rootX, c.Devices)
			ids := []string{}
			for _, d := range ds {
				ids = append(ids, d.ID)
			}
			if !reflect.DeepEqual(ids, c.IDs) {
				t.Fatalf("got %v want %v", ids, c.IDs)
			}
		})
	}
	ds, e := devicetrust.Verify(f.Principal, f.RootSigning, rootX, f.Devices)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Bundle.VerifyClaim(ds[1].Signing, f.Server, f.Principal, f.DeviceID, f.Bundle.Identity, f.KeyID, time.Unix(f.Now, 0)); e != nil {
		t.Fatal(e)
	}
	for _, c := range f.BundleCases {
		t.Run(c.Name, func(t *testing.T) {
			if _, e := c.Bundle.VerifyClaim(ds[1].Signing, f.Server, f.Principal, f.DeviceID, f.Bundle.Identity, f.KeyID, time.Unix(f.Now, 0)); e == nil {
				t.Fatal("accepted tampered bundle")
			}
		})
	}
	if _, e = f.Bundle.VerifyClaim(ds[1].Signing, f.Server, f.Principal, f.DeviceID, "changed", f.KeyID, time.Unix(f.Now, 0)); e == nil {
		t.Fatal("overwrote account pin")
	}
	if _, e := olmdir.ServerBinding(""); e == nil {
		t.Fatal("empty TLS binding accepted")
	}
	if olmdir.ValidCurve(base64.RawStdEncoding.EncodeToString(make([]byte, 32))) {
		t.Fatal("low order key accepted")
	}
}

func TestAuthenticatedServerBindingCanonicalization(t *testing.T) {
	want := "tls-sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, s := range []string{strings.Repeat("A", 64), strings.TrimSuffix(strings.Repeat("AA:", 32), ":")} {
		got, e := olmdir.ServerBinding(s)
		if e != nil || got != want {
			t.Fatalf("canonical binding %q %v", got, e)
		}
	}
	for _, s := range []string{"", "display.example", strings.Repeat("a", 63), strings.Repeat("aa:", 32)} {
		if _, e := olmdir.ServerBinding(s); e == nil {
			t.Fatal("accepted invalid fingerprint", s)
		}
	}
}
