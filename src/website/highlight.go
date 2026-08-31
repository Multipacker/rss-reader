package website

import (
	"slices"
	"strings"
)

type HighlightPart struct {
	Value     string
	Highlight bool
}

type HighlightString []HighlightPart

func highlightFromValueQuery(value string, queryWords []string) HighlightString {
	type Range struct {
		min, max int
	}

	lowerValue := strings.ToLower(value)

	// NOTE(simon): Collect matches.
	var matches []Range
	for _, queryWord := range queryWords {
		offset := 0
		for {
			start := offset + strings.Index(lowerValue[offset:], queryWord)
			if start < offset {
				break
			}

			matches = append(matches, Range{start, start + len(queryWord)})
			offset = start + len(queryWord)
		}
	}

	// NOTE(simon): Sort mathces on starting position.
	slices.SortFunc(matches, func (a, b Range) int {
		return a.min - b.min
	})

	// NOTE(simon): Build highlight string.
	var highlight HighlightString
	previousOffset := 0
	for _, match := range matches {
		if previousOffset < match.min {
			highlight = append(highlight, HighlightPart{value[previousOffset:match.min], false})
		}

		// NOTE(simon): If we have overlapping matches, keep the first one.
		if previousOffset <= match.min {
			highlight = append(highlight, HighlightPart{value[match.min:match.max], true})

			previousOffset = match.max
		}
	}
	if previousOffset != len(value) {
		highlight = append(highlight, HighlightPart{value[previousOffset:], false})
	}

	return highlight
}
