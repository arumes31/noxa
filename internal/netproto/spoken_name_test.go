package netproto

import "testing"

func TestSpokenNameRoundTrip(t *testing.T) {
	get := SpokenNameGet{ClientID: "c1", Language: "de"}
	var gotGet SpokenNameGet
	roundTrip(t, MsgSpokenNameGet, get, &gotGet)
	data := SpokenNameData{ClientID: "c1", Language: "de", Key: "ab12", DataBase64: "UklGRg=="}
	var gotData SpokenNameData
	roundTrip(t, MsgSpokenNameData, data, &gotData)
	if gotGet != get || gotData != data {
		t.Fatalf("round trip: %+v %+v", gotGet, gotData)
	}
}
