package server

import (
	"sync"
	"testing"

	"noxa/internal/authorization"
)

// The memo must answer exactly as the evaluator, also under concurrent use.
func TestMemoizedEvaluateMatchesEvaluator(t *testing.T) {
	e, err := authorization.NewRoleEvaluator(serverRoleFixture().policy)
	if err != nil {
		t.Fatal(err)
	}
	allowed := memoizedEvaluate(e)
	capabilities := []authorization.Capability{authorization.Connect, authorization.ViewChannel, authorization.Whisper, authorization.ViewConnectionInfo}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for round := range 2 {
				for userID := int64(0); userID <= 4; userID++ {
					for channelID := int64(0); channelID <= 3; channelID++ {
						for _, capability := range capabilities {
							if got, want := allowed(userID, channelID, capability), e.Evaluate(userID, channelID, capability).Allowed; got != want {
								t.Errorf("round %d user %d channel %d %s = %v, want %v", round, userID, channelID, capability, got, want)
							}
						}
					}
				}
			}
		})
	}
	wg.Wait()
}
