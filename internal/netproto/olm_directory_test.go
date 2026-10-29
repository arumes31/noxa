package netproto

import (
	"bytes"
	"noxa/internal/e2ee/olmdir"
	"testing"
)

func TestOlmDirectoryStrictWire(t *testing.T) {
	good := `{"request_id":"retry-1","claim_until":1900000000,"action":"claim","account_epoch":1,"principal":"bob","device_id":"phone"}`
	if _, e := DecodeOlmDirectoryRequest(&Frame{Payload: []byte(good)}); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{`{"request_id":"a","request_id":"b","claim_until":1900000000,"action":"claim","account_epoch":1,"principal":"bob","device_id":"phone"}`, good + ` {}`, `{"request_id":"a","claim_until":1900000000,"action":"claim","account_epoch":1,"principal":"bob","device_id":"phone","unknown":true}`, `{"request_id":"a","action":"publish","principal":"other","bundle":{}}`, `{"request_id":"a","claim_until":1900000000,"action":"claim","account_epoch":1,"principal":"bob","device_id":"phone","bundle":{}}`, string(bytes.Repeat([]byte(" "), olmdir.MaxBytes+1))} {
		if _, e := DecodeOlmDirectoryRequest(&Frame{Payload: []byte(bad)}); e == nil {
			t.Fatal("accepted malformed request")
		}
	}
}

func TestOlmDirectoryCanonicalWireNames(t *testing.T) {
	cases := map[string]string{
		"mixed case duplicate": `{"request_id":"a","Request_ID":"b","claim_until":1900000000,"action":"claim","account_epoch":1,"principal":"bob","device_id":"phone"}`,
		"uppercase duplicate":  `{"request_id":"a","REQUEST_ID":"b","claim_until":1900000000,"action":"claim","account_epoch":1,"principal":"bob","device_id":"phone"}`,
		"noncanonical only":    `{"Request_ID":"a","claim_until":1900000000,"action":"claim","account_epoch":1,"principal":"bob","device_id":"phone"}`,
		"nested duplicate":     `{"request_id":"a","action":"publish","bundle":{"signature":"","Signature":""}}`,
		"nested unicode fold":  `{"request_id":"a","action":"publish","bundle":{"signature":"","\u017fignature":""}}`,
		"unicode fold only":    `{"reque\u017ft_id":"a","claim_until":1900000000,"action":"claim","account_epoch":1,"principal":"bob","device_id":"phone"}`,
		"key entry case":       `{"request_id":"a","action":"publish","bundle":{"one_time_keys":[{"id":"a","ID":"b","key":"c"}]}}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeOlmDirectoryRequest(&Frame{Payload: []byte(payload)}); err == nil {
				t.Fatal("accepted noncanonical wire field")
			}
		})
	}
}

func TestOlmDirectoryOwnStateCarriesNoTarget(t *testing.T) {
	ok := []byte(`{"request_id":"own-1","action":"own_state"}`)
	if r, e := DecodeOlmDirectoryRequest(&Frame{Payload: ok}); e != nil || r.Action != "own_state" {
		t.Fatalf("own_state: %+v %v", r, e)
	}
	for _, bad := range []string{`{"request_id":"a","action":"own_state","principal":"bob"}`, `{"request_id":"a","action":"own_state","device_id":"d"}`, `{"request_id":"a","action":"own_state","account_epoch":1}`} {
		if _, e := DecodeOlmDirectoryRequest(&Frame{Payload: []byte(bad)}); e == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
