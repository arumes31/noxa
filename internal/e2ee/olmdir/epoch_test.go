package olmdir

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAccountEpochSignedAndRequired(t *testing.T) {
	data, e := os.ReadFile("testdata/directory-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Bundle Bundle `json:"bundle"`
	}
	if e = json.Unmarshal(data, &fixture); e != nil {
		t.Fatal(e)
	}
	b := fixture.Bundle
	b.AccountEpoch = 1
	first, e := b.Transcript()
	if e != nil {
		t.Fatal(e)
	}
	b.AccountEpoch = 2
	next, e := b.Transcript()
	if e != nil || string(first) == string(next) {
		t.Fatal("epoch not signed", e)
	}
	b.AccountEpoch = 0
	if _, e = b.Transcript(); e == nil {
		t.Fatal("missing epoch accepted")
	}
}
