package netproto

import (
	"bytes"
	"noxa/internal/e2ee/olmdir"
	"testing"
)

func TestOlmDirectoryStrictWire(t *testing.T) {
	good := `{"request_id":"retry-1","claim_until":1900000000,"action":"claim","principal":"bob","device_id":"phone"}`
	if _, e := DecodeOlmDirectoryRequest(&Frame{Payload: []byte(good)}); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{`{"request_id":"a","request_id":"b","claim_until":1900000000,"action":"claim","principal":"bob","device_id":"phone"}`, good + ` {}`, `{"request_id":"a","claim_until":1900000000,"action":"claim","principal":"bob","device_id":"phone","unknown":true}`, `{"request_id":"a","action":"publish","principal":"other","bundle":{}}`, `{"request_id":"a","claim_until":1900000000,"action":"claim","principal":"bob","device_id":"phone","bundle":{}}`, string(bytes.Repeat([]byte(" "), olmdir.MaxBytes+1))} {
		if _, e := DecodeOlmDirectoryRequest(&Frame{Payload: []byte(bad)}); e == nil {
			t.Fatal("accepted malformed request")
		}
	}
}
