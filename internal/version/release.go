package version

import (
	"fmt"
	"strings"
)

// NextRelease selects the next unused stable patch on the VERSION release line.
// Prerelease tags never advance the stable sequence.
func NextRelease(root string) (string, error) {
	base, err := readBaseVersion(root)
	if err != nil {
		return "", err
	}
	tags, err := (gitRunner{root: root}).text("tag", "--list")
	if err != nil {
		return "", fmt.Errorf("reading release tags: %w", err)
	}
	return nextRelease(base, strings.Fields(tags))
}

func nextRelease(base string, tags []string) (string, error) {
	baseline, valid := parseSemver(base)
	if !valid || len(baseline.prerelease) != 0 || strings.Contains(base, "+") {
		return "", fmt.Errorf("invalid stable release baseline %q", base)
	}
	patch := baseline.numbers[2]
	for _, tag := range tags {
		candidate, valid := parseSemver(tag)
		if !strings.HasPrefix(tag, "v") || !valid || len(candidate.prerelease) != 0 {
			continue
		}
		if candidate.numbers[0] > baseline.numbers[0] || candidate.numbers[0] == baseline.numbers[0] && candidate.numbers[1] > baseline.numbers[1] {
			return "", fmt.Errorf("stable tag %q is newer than the VERSION release line %q", tag, base)
		}
		if candidate.numbers[0] == baseline.numbers[0] && candidate.numbers[1] == baseline.numbers[1] && candidate.numbers[2] >= patch {
			if candidate.numbers[2] == int(^uint(0)>>1) {
				return "", fmt.Errorf("patch version exhausted for %q", tag)
			}
			patch = candidate.numbers[2] + 1
		}
	}
	return fmt.Sprintf("%d.%d.%d", baseline.numbers[0], baseline.numbers[1], patch), nil
}
