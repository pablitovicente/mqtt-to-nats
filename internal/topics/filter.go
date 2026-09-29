package topics

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ValidateFilter checks that filter is a valid MQTT topic filter: not empty, valid UTF-8
// without NUL, '#' only as the whole last level, and '+' only as a whole level.
func ValidateFilter(filter string) error {
	if filter == "" {
		return fmt.Errorf("topic filter is empty")
	}

	if !utf8.ValidString(filter) || strings.ContainsRune(filter, 0) {
		return fmt.Errorf("topic filter %q is not valid UTF-8 or contains NUL", filter)
	}

	levels := strings.Split(filter, "/")
	for index, level := range levels {
		if strings.Contains(level, "#") && (level != "#" || index != len(levels)-1) {
			return fmt.Errorf("topic filter %q: '#' must be the whole last level", filter)
		}

		if strings.Contains(level, "+") && level != "+" {
			return fmt.Errorf("topic filter %q: '+' must be a whole level", filter)
		}
	}

	return nil
}

// FiltersOverlap reports whether some topic matches both filters. Both must already have
// passed ValidateFilter.
func FiltersOverlap(first, second string) bool {
	firstLevels := strings.Split(first, "/")
	secondLevels := strings.Split(second, "/")

	// A wildcard in the first level doesn't match topics starting with '$', such as $SYS/...
	if startsWithDollarOnlyOnOneSide(firstLevels[0], secondLevels[0]) {
		return false
	}

	for index := 0; ; index++ {
		firstEnded := index == len(firstLevels)
		secondEnded := index == len(secondLevels)

		if firstEnded && secondEnded {
			return true
		}

		// '#' matches everything below, and the level above it too (a/# matches a).
		if !firstEnded && firstLevels[index] == "#" {
			return true
		}
		if !secondEnded && secondLevels[index] == "#" {
			return true
		}

		if firstEnded || secondEnded {
			return false
		}

		firstLevel := firstLevels[index]
		secondLevel := secondLevels[index]
		if firstLevel != "+" && secondLevel != "+" && firstLevel != secondLevel {
			return false
		}
	}
}

// startsWithDollarOnlyOnOneSide reports whether one first level is a literal starting with '$'
// and the other is a wildcard, which never match the same topic.
func startsWithDollarOnlyOnOneSide(firstLevel, secondLevel string) bool {
	firstIsWildcard := firstLevel == "+" || firstLevel == "#"
	secondIsWildcard := secondLevel == "+" || secondLevel == "#"

	firstHasDollar := strings.HasPrefix(firstLevel, "$")
	secondHasDollar := strings.HasPrefix(secondLevel, "$")

	return (firstHasDollar && secondIsWildcard) || (secondHasDollar && firstIsWildcard)
}

// NeedsPrefix reports whether a filter's stream subjects need a prefix. Without one:
//   - a first level of '+' or '#' gives stream subjects such as '*.>' or '>', which overlap
//     NATS's own $JS.API subjects, and NATS refuses to create the stream
//   - a first level starting with '$' could put messages on NATS's own subjects, such as
//     $JS.API.STREAM.DELETE.<name> for the topic $JS/API/STREAM/DELETE/<name>
func NeedsPrefix(filter string) bool {
	firstLevel, _, _ := strings.Cut(filter, "/")

	return firstLevel == "+" || firstLevel == "#" || strings.HasPrefix(firstLevel, "$")
}

// ValidatePrefix checks that prefix can go in front of every subject: dot-separated tokens,
// none empty, none a wildcard, no whitespace, valid UTF-8 without NUL or DEL.
func ValidatePrefix(prefix string) error {
	if !utf8.ValidString(prefix) || strings.ContainsAny(prefix, "\x00\x7f\t\n\f\r ") {
		return fmt.Errorf("prefix %q must be valid UTF-8 without whitespace, NUL or DEL", prefix)
	}

	for token := range strings.SplitSeq(prefix, ".") {
		if token == "" || token == "*" || token == ">" {
			return fmt.Errorf("prefix %q must be dot-separated names, none empty and none '*' or '>'", prefix)
		}
	}

	return nil
}

// StreamSubjects returns the subjects a stream needs to store every message matching filter.
// A filter ending in '#' also matches its parent level (a/# matches a), which NATS's '>' does
// not, so the parent gets its own subject. The filter must already have passed
// ValidateFilter, and the prefix ValidatePrefix when not empty.
func StreamSubjects(filter, prefix string) []string {
	subjects := []string{filterToSubject(prefix, filter)}

	parent, isMultiLevel := strings.CutSuffix(filter, "/#")
	if isMultiLevel && parent != "" {
		subjects = append(subjects, filterToSubject(prefix, parent))
	}

	return subjects
}

// SubjectsOverlap reports whether some NATS subject matches both subject filters, where '*'
// matches one token and '>' matches one or more tokens at the end.
func SubjectsOverlap(first, second string) bool {
	firstTokens := strings.Split(first, ".")
	secondTokens := strings.Split(second, ".")

	for index := 0; ; index++ {
		firstEnded := index == len(firstTokens)
		secondEnded := index == len(secondTokens)

		if firstEnded && secondEnded {
			return true
		}

		if firstEnded || secondEnded {
			return false
		}

		firstToken := firstTokens[index]
		secondToken := secondTokens[index]

		if firstToken == ">" || secondToken == ">" {
			return true
		}

		if firstToken != "*" && secondToken != "*" && firstToken != secondToken {
			return false
		}
	}
}
