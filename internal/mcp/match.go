package mcp

import (
	"fmt"
	"strings"
)

// pathMatch performs a glob-style match where:
//   - '*' matches any sequence of characters (including slashes)
//   - '?' matches exactly one character
//   - any other character must match literally
//
// The pattern is checked to be well-formed (i.e. it must end with a
// terminator when '#' is used, and '+' must occupy an entire path level).
func pathMatch(pattern, s string) (bool, error) {
	return wildcardMatch(pattern, s)
}

// wildcardMatch is a thin wrapper around strings.Match that first normalises
// the MQTT-style wildcards. '#' becomes '*' and '+' becomes '?' which
// matches a single character; we further restrict '+' to a single path
// segment by checking the surrounding slashes.
func wildcardMatch(pattern, s string) (bool, error) {
	if strings.Contains(pattern, "#") {
		// '#' in MQTT is "the rest of the topic", so if the pattern contains
		// '#' it must be the last character.
		idx := strings.Index(pattern, "#")
		if idx != len(pattern)-1 {
			return false, fmt.Errorf("'#' must be the last character in the pattern")
		}
		prefix := pattern[:idx]
		// Strip trailing slash from prefix to keep segment boundaries clean.
		prefix = strings.TrimSuffix(prefix, "/")
		if prefix == "" {
			return true, nil
		}
		return strings.HasPrefix(s, prefix), nil
	}

	// Validate '+' usage: it must occupy an entire path level.
	for _, seg := range strings.Split(pattern, "/") {
		if strings.Contains(seg, "+") && seg != "+" {
			return false, fmt.Errorf("'+' must be the only character in a path segment")
		}
	}

	// Translate MQTT wildcards: '+' -> '?' works for the single-character
	// case but we need a real segment-level match. Implement it manually.
	return matchSegments(strings.Split(pattern, "/"), strings.Split(s, "/")), nil
}

func matchSegments(pat, str []string) bool {
	if len(pat) == 0 {
		return len(str) == 0
	}
	if pat[0] == "#" {
		return true
	}
	if len(str) == 0 {
		return false
	}
	if pat[0] == "+" || pat[0] == str[0] {
		return matchSegments(pat[1:], str[1:])
	}
	return false
}