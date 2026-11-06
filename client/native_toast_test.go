package main

import (
	"encoding/xml"
	"errors"
	"strings"
	"testing"
	"time"

	"noxa/client/internal/wintoast"
)

// A fixed, public test token (not a secret): 32 lowercase hex characters.
var testToastToken = strings.Repeat("0123456789abcdef", 2)

func toastRequest() NativeToastRequest {
	return NativeToastRequest{Token: testToastToken, Conversation: "tab-1|dm:peer", Title: "Alice", Body: "hello", Replyable: true,
		ReplyLabel: "Reply", ReadLabel: "Mark as read", Placeholder: "Type a reply…"}
}

// toastDoc is the subset of the toast schema the tests inspect.
type toastDoc struct {
	Launch         string   `xml:"launch,attr"`
	ActivationType string   `xml:"activationType,attr"`
	Texts          []string `xml:"visual>binding>text"`
	Audio          struct {
		Silent string `xml:"silent,attr"`
	} `xml:"audio"`
	Inputs []struct {
		ID          string `xml:"id,attr"`
		Type        string `xml:"type,attr"`
		Placeholder string `xml:"placeHolderContent,attr"`
	} `xml:"actions>input"`
	Actions []struct {
		Content        string `xml:"content,attr"`
		Arguments      string `xml:"arguments,attr"`
		ActivationType string `xml:"activationType,attr"`
		InputID        string `xml:"hint-inputId,attr"`
	} `xml:"actions>action"`
}

func parseToastDoc(t *testing.T, content string) toastDoc {
	t.Helper()
	var doc toastDoc
	if err := xml.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatalf("toast XML does not parse: %v\n%s", err, content)
	}
	return doc
}

func TestBuildToastXMLHasReplyAndReadActions(t *testing.T) {
	content, err := buildToastXML(toastRequest())
	if err != nil {
		t.Fatal(err)
	}
	doc := parseToastDoc(t, content)
	if doc.Launch != "noxa-toast:v1:open:"+testToastToken || doc.ActivationType != "foreground" {
		t.Fatalf("launch = %q %q", doc.Launch, doc.ActivationType)
	}
	if len(doc.Texts) != 2 || doc.Texts[0] != "Alice" || doc.Texts[1] != "hello" || doc.Audio.Silent != "true" {
		t.Fatalf("visual = %+v audio=%+v", doc.Texts, doc.Audio)
	}
	if len(doc.Inputs) != 1 || doc.Inputs[0].ID != "reply" || doc.Inputs[0].Type != "text" || doc.Inputs[0].Placeholder != "Type a reply…" {
		t.Fatalf("inputs = %+v", doc.Inputs)
	}
	if len(doc.Actions) != 2 ||
		doc.Actions[0].Arguments != "noxa-toast:v1:reply:"+testToastToken || doc.Actions[0].InputID != "reply" || doc.Actions[0].ActivationType != "background" ||
		doc.Actions[1].Arguments != "noxa-toast:v1:read:"+testToastToken || doc.Actions[1].Content != "Mark as read" {
		t.Fatalf("actions = %+v", doc.Actions)
	}
}

func TestBuildToastXMLEscapesEverythingUserControlled(t *testing.T) {
	req := toastRequest()
	req.Title = `</text><action arguments="noxa-toast:v1:read:ffffffffffffffffffffffffffffffff"/>`
	req.Body = "a & b < c > d \"q\" 'x' \x00\x07 end"
	req.ReplyLabel = `"/><input id="evil`
	req.Placeholder = `x" activationType="protocol`
	content, err := buildToastXML(req)
	if err != nil {
		t.Fatal(err)
	}
	doc := parseToastDoc(t, content)
	if doc.Texts[0] != req.Title {
		t.Fatalf("title not preserved as text: %q", doc.Texts[0])
	}
	if !strings.Contains(doc.Texts[1], "a & b < c > d \"q\" 'x' ") || strings.ContainsAny(doc.Texts[1], "\x00\x07") {
		t.Fatalf("body = %q", doc.Texts[1])
	}
	if len(doc.Inputs) != 1 || len(doc.Actions) != 2 || doc.Actions[0].Content != req.ReplyLabel || doc.Inputs[0].Placeholder != req.Placeholder {
		t.Fatalf("injection changed the structure: %+v %+v", doc.Inputs, doc.Actions)
	}
	if strings.Count(content, `arguments="noxa-toast:v1:`) != 2 || strings.Count(content, `launch="noxa-toast:v1:`) != 1 {
		t.Fatalf("only the three generated arguments may appear unescaped:\n%s", content)
	}
}

func TestBuildToastXMLWithoutReplyAndWithDefaults(t *testing.T) {
	req := NativeToastRequest{Token: testToastToken, Conversation: "c", Body: strings.Repeat("x", 2000)}
	content, err := buildToastXML(req)
	if err != nil {
		t.Fatal(err)
	}
	doc := parseToastDoc(t, content)
	if len(doc.Inputs) != 0 || len(doc.Actions) != 1 || doc.Actions[0].Content != "Mark as read" {
		t.Fatalf("non-replyable toast: %+v %+v", doc.Inputs, doc.Actions)
	}
	if doc.Texts[0] != "noXa" || len([]rune(doc.Texts[1])) != nativeToastMaxBody {
		t.Fatalf("title %q, body length %d", doc.Texts[0], len([]rune(doc.Texts[1])))
	}
	for _, bad := range []NativeToastRequest{
		{Token: "short", Conversation: "c"},
		{Token: strings.ToUpper(testToastToken), Conversation: "c"},
		{Token: testToastToken, Conversation: "  "},
	} {
		if _, err := buildToastXML(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestParseToastActivation(t *testing.T) {
	reply := []wintoast.UserData{{Key: "other", Value: "ignored"}, {Key: "reply", Value: "  on my way  "}}
	got, err := parseToastActivation("noxa-toast:v1:reply:"+testToastToken, reply)
	if err != nil || got.Action != "reply" || got.Token != testToastToken || got.Reply != "on my way" {
		t.Fatalf("reply = %+v %v", got, err)
	}
	for _, action := range []string{"open", "read"} {
		if got, err := parseToastActivation("noxa-toast:v1:"+action+":"+testToastToken, reply); err != nil || got.Action != action || got.Reply != "" {
			t.Fatalf("%s = %+v %v", action, got, err)
		}
	}
	rejected := []struct {
		args   string
		inputs []wintoast.UserData
	}{
		{"", nil},
		{"noxa-toast:v2:open:" + testToastToken, nil},
		{"noxa-toast:v1:delete:" + testToastToken, nil},
		{"noxa-toast:v1:open:" + testToastToken + ":x", nil},
		{"noxa-toast:v1:open:" + strings.ToUpper(testToastToken), nil},
		{"noxa-toast:v1:open:../../x", nil},
		{" noxa-toast:v1:open:" + testToastToken, nil},
		{"noxa-toast:v1:reply:" + testToastToken, []wintoast.UserData{{Key: "reply", Value: "   "}}},
		{"noxa-toast:v1:reply:" + testToastToken, []wintoast.UserData{{Key: "reply", Value: strings.Repeat("y", nativeToastMaxReply+1)}}},
		{"noxa-toast:v1:reply:" + testToastToken, []wintoast.UserData{{Key: "reply", Value: "bad \xff utf8"}}},
	}
	for _, tc := range rejected {
		if got, err := parseToastActivation(tc.args, tc.inputs); err == nil {
			t.Fatalf("accepted %q: %+v", tc.args, got)
		}
	}
	if _, err := parseToastActivation("noxa-toast:v1:reply:"+testToastToken, nil); err == nil {
		t.Fatal("a reply without text was accepted")
	}
}

func TestNativeToastTokensAreSingleUseBoundedAndExpire(t *testing.T) {
	var state nativeToastState
	now := time.Unix(1_800_000_000, 0)
	state.issue(testToastToken, "conv", now)
	if conv, ok := state.take(testToastToken, now); !ok || conv != "conv" {
		t.Fatalf("take = %q %v", conv, ok)
	}
	if _, ok := state.take(testToastToken, now); ok {
		t.Fatal("a token acted twice")
	}
	state.issue(testToastToken, "conv", now)
	if _, ok := state.take(testToastToken, now.Add(nativeToastTokenTTL)); ok {
		t.Fatal("an expired token acted")
	}
	for i := range nativeToastMaxTokens + 20 {
		token := strings.Repeat("0", 24) + strings.Repeat("a", 8)
		token = token[:24] + hexByte(i>>8) + hexByte(i) + "aaaa"
		state.issue(token, "c", now.Add(time.Duration(i)*time.Second))
	}
	if len(state.tokens) != nativeToastMaxTokens {
		t.Fatalf("tokens = %d, want bound %d", len(state.tokens), nativeToastMaxTokens)
	}
	state.issue(testToastToken, "keep", now.Add(time.Hour))
	state.forget("c")
	if len(state.tokens) != 1 {
		t.Fatalf("forget left %d tokens", len(state.tokens))
	}
	state.forget("")
	if len(state.tokens) != 0 {
		t.Fatal("forget all left tokens")
	}
}

func hexByte(v int) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[(v>>4)&15], digits[v&15]})
}

func TestConversationToastTagIsStableAndOpaque(t *testing.T) {
	a, b := conversationToastTag("tab|dm:alice"), conversationToastTag("tab|dm:bob")
	if a != conversationToastTag("tab|dm:alice") || a == b || len(a) != 32 || strings.Contains(a, "alice") {
		t.Fatalf("tags %q %q", a, b)
	}
}

func withToastSeams(t *testing.T) (*[]wintoast.Toast, *[]string, *[]NativeToastActivation, *int) {
	t.Helper()
	shown, removed, emitted, raised := []wintoast.Toast{}, []string{}, []NativeToastActivation{}, 0
	oldShow, oldRemove, oldClear, oldRaise, oldEmit := nativeToastShow, nativeToastRemove, nativeToastClear, nativeToastRaise, nativeToastEmit
	nativeToastShow = func(appID string, toast wintoast.Toast) error {
		if appID != nativeToastAppID {
			t.Fatalf("appID %q", appID)
		}
		shown = append(shown, toast)
		return nil
	}
	nativeToastRemove = func(_, group, tag string) error { removed = append(removed, group+"/"+tag); return nil }
	nativeToastClear = func(string) error { removed = append(removed, "*"); return nil }
	nativeToastRaise = func(*App) { raised++ }
	nativeToastEmit = func(_ *App, activation NativeToastActivation) { emitted = append(emitted, activation) }
	t.Cleanup(func() {
		nativeToastShow, nativeToastRemove, nativeToastClear, nativeToastRaise, nativeToastEmit = oldShow, oldRemove, oldClear, oldRaise, oldEmit
	})
	return &shown, &removed, &emitted, &raised
}

func TestShowAndClearNativeToasts(t *testing.T) {
	shown, removed, _, _ := withToastSeams(t)
	a := &App{}
	if msg := a.ShowNativeToast(toastRequest()); msg == "" {
		t.Fatal("showed a toast before registration")
	}
	a.toasts.available = true
	if msg := a.ShowNativeToast(toastRequest()); msg != "" {
		t.Fatal(msg)
	}
	bad := toastRequest()
	bad.Token = "nope"
	if msg := a.ShowNativeToast(bad); msg == "" {
		t.Fatal("accepted a malformed token")
	}
	tag := conversationToastTag("tab-1|dm:peer")
	if len(*shown) != 1 || (*shown)[0].Tag != tag || (*shown)[0].Group != nativeToastGroup {
		t.Fatalf("shown = %+v", *shown)
	}
	a.ClearNativeToasts("tab-1|dm:peer")
	a.ClearNativeToasts("")
	if strings.Join(*removed, ",") != nativeToastGroup+"/"+tag+",*" {
		t.Fatalf("removed = %v", *removed)
	}
	if _, ok := a.toasts.take(testToastToken, time.Now()); ok {
		t.Fatal("clearing a conversation kept its token")
	}
}

func TestHandleToastActivationRoutesOnlyIssuedTokens(t *testing.T) {
	_, _, emitted, raised := withToastSeams(t)
	a := &App{}
	now := time.Now()
	a.toasts.issue(testToastToken, "tab-1|dm:peer", now)

	a.handleToastActivation("noxa-toast:v1:reply:"+testToastToken, []wintoast.UserData{{Key: "reply", Value: "hi"}}, now)
	if len(*emitted) != 1 || (*emitted)[0] != (NativeToastActivation{Token: testToastToken, Action: "reply", Conversation: "tab-1|dm:peer", Reply: "hi"}) || *raised != 0 {
		t.Fatalf("reply: emitted=%+v raised=%d", *emitted, *raised)
	}
	// Used tokens, unknown tokens and forged arguments are never forwarded.
	a.handleToastActivation("noxa-toast:v1:read:"+testToastToken, nil, now)
	a.handleToastActivation("noxa-toast:v1:reply:ffffffffffffffffffffffffffffffff", []wintoast.UserData{{Key: "reply", Value: "x"}}, now)
	a.handleToastActivation("noxa-toast:v1:send:"+testToastToken, nil, now)
	if len(*emitted) != 1 || *raised != 0 {
		t.Fatalf("forwarded a rejected activation: %+v raised=%d", *emitted, *raised)
	}
	// Clicking a toast from an earlier run still brings noXa forward.
	a.handleToastActivation("noxa-toast:v1:open:ffffffffffffffffffffffffffffffff", nil, now)
	a.toasts.issue(testToastToken, "tab-1|group:g", now)
	a.handleToastActivation("noxa-toast:v1:open:"+testToastToken, nil, now)
	if *raised != 2 || len(*emitted) != 2 || (*emitted)[1].Action != "open" || (*emitted)[1].Conversation != "tab-1|group:g" {
		t.Fatalf("open: emitted=%+v raised=%d", *emitted, *raised)
	}
}

func TestShowNativeToastRetiresReplacedTokensOnlyOnSuccess(t *testing.T) {
	shown, _, _, _ := withToastSeams(t)
	a := &App{}
	a.toasts.available = true
	first, second, other := toastRequest(), toastRequest(), toastRequest()
	second.Token = strings.Repeat("1", 32)
	other.Token, other.Conversation = strings.Repeat("2", 32), "tab-1|dm:someone-else"
	for _, req := range []NativeToastRequest{first, other, second} {
		if msg := a.ShowNativeToast(req); msg != "" {
			t.Fatal(msg)
		}
	}
	if len(*shown) != 3 {
		t.Fatalf("shown %d", len(*shown))
	}
	if _, ok := a.toasts.take(first.Token, time.Now()); ok {
		t.Fatal("the replaced toast's token still acts")
	}
	if _, ok := a.toasts.take(other.Token, time.Now()); !ok {
		t.Fatal("another conversation's token was retired")
	}
	// A failed replacement keeps the toast on screen working.
	nativeToastShow = func(string, wintoast.Toast) error { return errors.New("boom") }
	third := toastRequest()
	third.Token = strings.Repeat("3", 32)
	if msg := a.ShowNativeToast(third); msg == "" {
		t.Fatal("a failed show reported success")
	}
	if _, ok := a.toasts.take(third.Token, time.Now()); ok {
		t.Fatal("the failed toast's token was kept")
	}
	if _, ok := a.toasts.take(second.Token, time.Now()); !ok {
		t.Fatal("a failed replacement retired the visible toast's token")
	}
}

func TestOpenActivationIsEmittedBeforeTheWindowIsRaised(t *testing.T) {
	var order []string
	oldRaise, oldEmit := nativeToastRaise, nativeToastEmit
	nativeToastRaise = func(*App) { order = append(order, "raise") }
	nativeToastEmit = func(*App, NativeToastActivation) { order = append(order, "emit") }
	t.Cleanup(func() { nativeToastRaise, nativeToastEmit = oldRaise, oldEmit })
	a := &App{}
	a.toasts.issue(testToastToken, "c", time.Now())
	a.handleToastActivation("noxa-toast:v1:open:"+testToastToken, nil, time.Now())
	if strings.Join(order, ",") != "emit,raise" {
		t.Fatalf("order = %v", order)
	}
}
