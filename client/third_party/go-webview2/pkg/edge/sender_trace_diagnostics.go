package edge

import "encoding/json"

const senderTraceDiagnosticLimit = 131072

var senderTraceKinds = [...]string{"encode", "encoded", "enqueue", "process"}
var senderTracePhases = [...]string{"B", "X", "I", "i", "E", "other"}
var senderTraceScopes = [...]string{"global", "process", "thread", "absent", "other"}
var senderTraceIDStates = [...]string{"absent", "invalid", "zero", "positive"}

// Fixed vocabulary and bounded counters only. PID/TID comparisons describe
// trace representation, not ownership: global instant tracks can have different
// IDs from thread tracks. They are still rejected by the strict timing parser.
type senderTraceDiagnostics struct {
	ScanLimit               int                                    `json:"scan_limit"`
	EntriesExamined         int                                    `json:"entries_examined"`
	EntriesAfterFreeze      int                                    `json:"entries_after_freeze"`
	MalformedEntries        int                                    `json:"malformed_entries"`
	ClassificationTruncated bool                                   `json:"classification_truncated"`
	ByKind                  map[string]*senderTraceKindDiagnostics `json:"by_kind"`
	FirstRejection          *senderTraceRejection                  `json:"first_rejection,omitempty"`
}
type senderTraceKindDiagnostics struct {
	Seen             int            `json:"seen"`
	CategoryExpected int            `json:"category_expected"`
	CategoryOther    int            `json:"category_other"`
	Phases           map[string]int `json:"phases"`
	Scopes           map[string]int `json:"scopes"`
	PIDStates        map[string]int `json:"pid_states"`
	TIDStates        map[string]int `json:"tid_states"`
}
type senderTraceRejection struct {
	Kind         string `json:"kind"`
	Check        string `json:"check"`
	InstantScope string `json:"instant_scope"`
	PIDRelation  string `json:"pid_relation"`
	TIDRelation  string `json:"tid_relation"`
}

func traceCounters(keys []string) map[string]int {
	counts := make(map[string]int, len(keys))
	for _, key := range keys {
		counts[key] = 0
	}
	return counts
}
func (p *senderTraceParser) ensureDiagnostics() {
	if p.diagnostics != nil {
		return
	}
	d := &senderTraceDiagnostics{ScanLimit: senderTraceDiagnosticLimit, ByKind: map[string]*senderTraceKindDiagnostics{}}
	for _, kind := range senderTraceKinds {
		d.ByKind[kind] = &senderTraceKindDiagnostics{Phases: traceCounters(senderTracePhases[:]), Scopes: traceCounters(senderTraceScopes[:]), PIDStates: traceCounters(senderTraceIDStates[:]), TIDStates: traceCounters(senderTraceIDStates[:])}
	}
	p.diagnostics = d
}
func traceInstantScope(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "absent"
	}
	var scope string
	if json.Unmarshal(raw, &scope) != nil {
		return "other"
	}
	switch scope {
	case "g":
		return "global"
	case "p":
		return "process"
	case "t":
		return "thread"
	default:
		return "other"
	}
}
func traceIDState(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "absent"
	}
	n, ok := traceUint(raw, 9007199254740991)
	if !ok {
		return "invalid"
	}
	if n == 0 {
		return "zero"
	}
	return "positive"
}
func (p *senderTraceParser) classify(kind string, e senderTraceInput) {
	d := p.diagnostics.ByKind[kind]
	d.Seen++
	if e.Cat == senderTraceCategory(kind) {
		d.CategoryExpected++
	} else {
		d.CategoryOther++
	}
	phase := e.Ph
	if _, ok := d.Phases[phase]; !ok {
		phase = "other"
	}
	d.Phases[phase]++
	d.Scopes[traceInstantScope(e.Scope)]++
	d.PIDStates[traceIDState(e.PID)]++
	d.TIDStates[traceIDState(e.TID)]++
}
func traceIDRelation(raw json.RawMessage, reference *uint64) string {
	n, ok := traceUint(raw, 9007199254740991)
	if !ok || reference == nil {
		return "unavailable"
	}
	if n == *reference {
		return "same"
	}
	return "different"
}
func (p *senderTraceParser) rejection(kind, check string, e senderTraceInput) *senderTraceRejection {
	pid, tid := p.rendererPID, (*uint64)(nil)
	if old, ok := p.producer[kind]; ok {
		pid, tid = &old[0], &old[1]
	}
	return &senderTraceRejection{Kind: kind, Check: check, InstantScope: traceInstantScope(e.Scope), PIDRelation: traceIDRelation(e.PID, pid), TIDRelation: traceIDRelation(e.TID, tid)}
}
