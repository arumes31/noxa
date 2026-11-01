package webrtc

import (
	"fmt"
	"strconv"
	"strings"
)

// stabilizeOfferExtensions keeps established IDs and puts never-negotiated
// server extensions at the highest free two-byte IDs, above IDs in Chromium's
// initial offers. Chromium retains its allocated one-byte IDs after rolling back a
// crossing first-video offer, so Pion's default low-ID allocation can collide.
//
// Pion commits its generated offer locally. The wire offer uses the new IDs;
// its answer then installs those exact IDs in Pion's media engine before RTP
// starts. Subsequent offers retain the mappings established by that answer.
func stabilizeOfferExtensions(offer, remote string) (string, error) {
	type extension struct{ id, uri string }
	parse := func(line string) (extension, bool) {
		if !strings.HasPrefix(line, "a=extmap:") {
			return extension{}, false
		}
		fields := strings.Fields(strings.TrimPrefix(line, "a=extmap:"))
		if len(fields) < 2 {
			return extension{}, false
		}
		rawID, _, _ := strings.Cut(fields[0], "/")
		id, err := strconv.Atoi(rawID)
		if err != nil || id < 1 || id > 246 {
			return extension{}, false
		}
		return extension{strconv.Itoa(id), fields[1]}, true
	}
	established := make(map[extension]bool)
	used := make(map[string]bool)
	for line := range strings.SplitSeq(remote, "\n") {
		if ext, ok := parse(line); ok {
			established[ext] = true
			used[ext.id] = true
		}
	}
	lines := strings.Split(offer, "\r\n")
	for _, line := range lines {
		if ext, ok := parse(line); ok {
			used[ext.id] = true
		}
	}
	replacements := make(map[extension]string)
	// pion/sdp v3.0.20 accepts extension IDs only through 246.
	nextID := 246
	for i, line := range lines {
		ext, ok := parse(line)
		if !ok || established[ext] {
			continue
		}
		id := replacements[ext]
		if id == "" {
			for nextID >= 16 && used[strconv.Itoa(nextID)] {
				nextID--
			}
			if nextID < 16 {
				return "", fmt.Errorf("webrtc: no free two-byte RTP extension ID")
			}
			id = strconv.Itoa(nextID)
			used[id] = true
			replacements[ext] = id
			nextID--
		}
		value := strings.TrimPrefix(line, "a=extmap:")
		idEnd := strings.IndexAny(value, "/ 	")
		lines[i] = "a=extmap:" + id + value[idEnd:]
	}
	return strings.Join(lines, "\r\n"), nil
}
