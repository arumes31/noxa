package netproto

const MaxChatFilterListBytes = 4096

// ValidLists bounds each supplied list before normalization. Nil means retain;
// an empty string clears a list. Native callers may submit an empty patch.
func (p ChatFilterSet) ValidLists() bool {
	for _, value := range []*string{p.WordFilter, p.LinkBlacklist, p.LinkWhitelist} {
		if value != nil && len(*value) > MaxChatFilterListBytes {
			return false
		}
	}
	return true
}
