package broadcast

import (
	"encoding/json"
	"noxa/internal/state"
	"testing"
)

func TestSnapshotIdentifiesKeyDerivedGuests(t *testing.T) {
	for _, userID := range []int64{0, 42} {
		info := clientToInfo(&state.Client{ClientID: "session", UniqueID: "base64-key-identity", UserID: userID, Nickname: "Member"})
		data, err := json.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err = json.Unmarshal(data, &wire); err != nil {
			t.Fatal(err)
		}
		if wire["is_guest"] != (userID == 0) {
			t.Fatalf("guest marker missing or incorrect: %s", data)
		}
		if _, ok := wire["user_id"]; ok {
			t.Fatal("internal account ID was exposed")
		}
	}
}
