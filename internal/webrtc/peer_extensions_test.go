package webrtc

import (
	"fmt"
	"strings"
	"testing"
)

func TestOfferExtensionIDsPreserveNegotiationAndAvoidOccupiedIDs(t *testing.T) {
	remote := "m=audio 9 RTP/AVP 111\r\na=extmap:3 twcc\r\na=extmap:246 occupied\r\na=extmap:245 occupied-too\r\n"
	offer := "m=audio 9 RTP/AVP 111\r\na=extmap:3 twcc\r\nm=video 9 RTP/AVP 96\r\na=extmap:3 twcc\r\na=extmap:5/recvonly rid attributes\r\na=extmap:6 repaired\r\nm=video 9 RTP/AVP 96\r\na=extmap:5/recvonly rid attributes\r\na=extmap:6 repaired\r\n"
	got, err := stabilizeOfferExtensions(offer, remote)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(strings.ReplaceAll(offer, "extmap:5/recvonly", "extmap:244/recvonly"), "extmap:6 repaired", "extmap:243 repaired")
	if got != want {
		t.Fatalf("offer extensions:\n%s\nwant:\n%s", got, want)
	}
	// An accepted answer pins these mappings for every subsequent offer.
	again, err := stabilizeOfferExtensions(got, got)
	if err != nil || again != got {
		t.Fatalf("established mappings changed: %v\n%s", err, again)
	}
}

func TestOfferExtensionIDsExhaustionFailsClosed(t *testing.T) {
	var remote strings.Builder
	for id := 16; id <= 246; id++ {
		fmt.Fprintf(&remote, "a=extmap:%d existing-%d\r\n", id, id)
	}
	if _, err := stabilizeOfferExtensions("a=extmap:5 rid\r\n", remote.String()); err == nil {
		t.Fatal("exhausted IDs silently reused")
	}
}

func TestOfferExtensionIDsAcceptRemoteLFAndNumericIDs(t *testing.T) {
	remote := "a=extmap:0246 rid\na=extmap:0245 occupied\n"
	offer := "a=extmap:246 rid\r\na=extmap:005/recvonly repaired attributes\r\n"
	got, err := stabilizeOfferExtensions(offer, remote)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a=extmap:246 rid\r\na=extmap:244/recvonly repaired attributes\r\n"; got != want {
		t.Fatalf("numeric/LF mapping = %q, want %q", got, want)
	}
}

func TestOfferExtensionIDsCanReuseAnUnestablishedGeneratedID(t *testing.T) {
	var remote strings.Builder
	for id := 16; id < 246; id++ {
		fmt.Fprintf(&remote, "a=extmap:%d established-%d\r\n", id, id)
	}
	const offer = "a=extmap:246 new-rid\r\n"
	got, err := stabilizeOfferExtensions(offer, remote.String())
	if err != nil || got != offer {
		t.Fatalf("last free ID was reserved by its own replaced offer line: %v, %q", err, got)
	}
}

func TestOfferExtensionIDsDoNotReuseAnEstablishedIDForAnotherURI(t *testing.T) {
	const remote = "a=extmap:3 twcc\r\n"
	const offer = "a=extmap:3 twcc\r\na=extmap:3 different-uri\r\n"
	got, err := stabilizeOfferExtensions(offer, remote)
	if err != nil || got != "a=extmap:3 twcc\r\na=extmap:246 different-uri\r\n" {
		t.Fatalf("different URI inherited an established ID: %v, %q", err, got)
	}
}
