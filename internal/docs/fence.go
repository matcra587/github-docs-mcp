package docs

import "strings"

type markdownFence struct {
	character byte
	length    int
}

func (fence *markdownFence) closes(character byte, length int, rest string) bool {
	return character == fence.character && length >= fence.length && strings.TrimSpace(rest) == ""
}

func (fence *markdownFence) consume(line string) bool {
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

	return true
}
