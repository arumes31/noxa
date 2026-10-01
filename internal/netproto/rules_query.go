package netproto

// RulesInspection counts acceptances for exactly the returned wording/hash.
type RulesInspection struct {
	Text            string `json:"text"`
	Hash            string `json:"hash"`
	AcceptedClients int    `json:"accepted_clients"`
}
