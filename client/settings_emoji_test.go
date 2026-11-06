package main

import (
	"strings"
	"testing"
)

func TestNormalizeSettingsBoundsEmojiLists(t *testing.T) {
	many := make([]string, 0, 60)
	for i := range 60 {
		many = append(many, strings.Repeat("x", i%5+1)+string(rune('a'+i%26))+string(rune('A'+i/26)))
	}
	s := normalizeSettings(Settings{
		EmojiRecents:   append([]string{"😄", "", "😄", strings.Repeat("y", 65)}, many...),
		EmojiFavorites: many,
	})
	if len(s.EmojiRecents) != 24 || s.EmojiRecents[0] != "😄" || s.EmojiRecents[1] != many[0] {
		t.Fatalf("recents = %q", s.EmojiRecents)
	}
	if len(s.EmojiFavorites) != 48 {
		t.Fatalf("favorites length = %d, want 48", len(s.EmojiFavorites))
	}
	if got := normalizeSettings(Settings{}).EmojiRecents; got != nil {
		t.Fatalf("empty recents = %q, want nil", got)
	}
}
