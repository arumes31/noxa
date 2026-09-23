package netproto

import (
	"strings"
	"testing"
	"time"
)

func TestPollDefinitionValidation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	valid := PollDefinition{Question: "When?", Options: []string{"Today", "Tomorrow"}, ClosesAt: now.Add(time.Hour).Unix()}
	for _, tc := range []struct {
		name   string
		change func(*PollDefinition)
	}{
		{"blank question", func(p *PollDefinition) { p.Question = "  " }},
		{"long question", func(p *PollDefinition) { p.Question = strings.Repeat("a", 301) }},
		{"one option", func(p *PollDefinition) { p.Options = []string{"A"} }},
		{"duplicate options", func(p *PollDefinition) { p.Options = []string{"Today", " today "} }},
		{"empty option", func(p *PollDefinition) { p.Options = []string{"A", " "} }},
		{"expired", func(p *PollDefinition) { p.ClosesAt = now.Unix() }},
		{"too long", func(p *PollDefinition) { p.ClosesAt = now.Add(8 * 24 * time.Hour).Unix() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.change(&p)
			if p.Validate(now) == nil {
				t.Fatal("invalid poll accepted")
			}
		})
	}
	if err := valid.Validate(now); err != nil {
		t.Fatal(err)
	}
	body, err := EncodePoll(valid)
	if err != nil {
		t.Fatal(err)
	}
	got, isPoll, err := DecodePoll(body)
	if err != nil || !isPoll || got.Question != valid.Question {
		t.Fatalf("decode: %+v %t %v", got, isPoll, err)
	}
	if _, isPoll, err := DecodePoll("ordinary message"); isPoll || err != nil {
		t.Fatal("ordinary message treated as poll")
	}
	if _, _, err := DecodePoll(PollPrefix + `{"question":"bad", "unknown":true}`); err == nil {
		t.Fatal("unknown field accepted")
	}
}
