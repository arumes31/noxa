package connectionbenchmark

import (
	"errors"
	"strings"

	"noxa/internal/netproto"
)

// Pion pairs every remote candidate with every local one using linear
// checklist scans, so peer-supplied candidates and discovery servers are
// bounded before they reach it. A real Noxa server advertises a handful of
// each; the bounds leave generous headroom.
const (
	maxICEServerURLs      = 16
	maxRemoteICECandidate = 64
)

var errTooManyCandidates = errors.New("server sent too many media candidates")

// boundedICEServers deduplicates the server-supplied discovery URLs and keeps at
// most maxICEServerURLs of them across all entries.
func boundedICEServers(servers []netproto.ICEServer) []netproto.ICEServer {
	seen := make(map[string]struct{})
	var out []netproto.ICEServer
	for _, server := range servers {
		var urls []string
		for _, url := range server.URLs {
			if _, dup := seen[url]; dup || len(seen) >= maxICEServerURLs {
				continue
			}
			seen[url] = struct{}{}
			urls = append(urls, url)
		}
		if len(urls) > 0 {
			server.URLs = urls
			out = append(out, server)
		}
	}
	return out
}

// candidateBudget counts distinct remote candidates across the initial answer,
// later offers and trickled candidates. Repeated candidates (renegotiation
// SDP re-lists the gathered set) are not charged twice.
type candidateBudget struct {
	seen map[string]struct{}
}

func (b *candidateBudget) admit(candidate string) error {
	candidate = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(candidate), "a="))
	if candidate == "" {
		return nil
	}
	if b.seen == nil {
		b.seen = make(map[string]struct{})
	}
	if _, ok := b.seen[candidate]; ok {
		return nil
	}
	if len(b.seen) >= maxRemoteICECandidate {
		return errTooManyCandidates
	}
	b.seen[candidate] = struct{}{}
	return nil
}

// admitSDP charges every candidate attribute in a remote description.
func (b *candidateBudget) admitSDP(sdp string) error {
	for line := range strings.Lines(sdp) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "a=candidate:") {
			if err := b.admit(line); err != nil {
				return err
			}
		}
	}
	return nil
}
