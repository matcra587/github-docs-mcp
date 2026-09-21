package docs

import (
	"regexp"
	"strings"
)

var listFenceMarker = regexp.MustCompile(`^ {0,3}(?:[-+*]|[0-9]{1,9}[.)])([ \t]+)`)

type markdownFence struct {
	character byte
	length    int
	indent    int
}

func (fence *markdownFence) closes(character byte, length int, rest string) bool {
	return character == fence.character && length >= fence.length && strings.TrimSpace(rest) == ""
}

func (fence *markdownFence) consume(line string) bool {
	if fence.length > 0 && fence.indent > 0 {
		if strings.TrimSpace(line) == "" {
			return true
		}

		var inside bool
		line, inside = stripFenceIndent(line, fence.indent)
		if !inside {
			*fence = markdownFence{}
		}
	}

	indent := 0
	if fence.length == 0 {
		if marker := listFenceMarker.FindStringSubmatchIndex(line); marker != nil {
			start := fenceColumns(line[:marker[2]])
			end := fenceColumns(line[:marker[1]])
			if end-start <= 4 {
				indent = end
				line = line[marker[1]:]
			}
		}
	}

	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 {
		return fence.length > 0
	}

	character := trimmed[0]
	if character != '`' && character != '~' {
		return fence.length > 0
	}

	length := 0
	for length < len(trimmed) && trimmed[length] == character {
		length++
	}

	if fence.length > 0 {
		if fence.closes(character, length, trimmed[length:]) {
			fence.length = 0
		}

		return true
	}

	if length < 3 || (character == '`' && strings.Contains(trimmed[length:], "`")) {
		return false
	}

	fence.character, fence.length = character, length
	fence.indent = indent

	return true
}

func fenceColumns(prefix string) int {
	columns := 0
	for _, character := range prefix {
		if character == '\t' {
			columns += 4 - columns%4
		} else {
			columns++
		}
	}

	return columns
}

func stripFenceIndent(line string, want int) (string, bool) {
	columns := 0
	for position, character := range line {
		switch character {
		case ' ':
			columns++
		case '\t':
			columns += 4 - columns%4
		default:
			return line, false
		}

		if columns >= want {
			return strings.Repeat(" ", columns-want) + line[position+1:], true
		}
	}

	return line, false
}
