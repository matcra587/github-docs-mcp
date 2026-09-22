package docs

import (
	"bytes"
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var headingAutolink = regexp.MustCompile(`^(?:[A-Za-z][A-Za-z0-9+.-]{1,31}:[^\x00-\x20\x7f<>]*|` +
	"[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?" +
	`(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*)$`)

type headingRange struct {
	level int
	text  string
	start int
}

func headingRanges(body []byte) []headingRange {
	var (
		headings []headingRange
		fence    markdownFence
	)

	previousStart := -1
	previous := ""

	for offset := 0; offset < len(body); {
		end := bytes.IndexByte(body[offset:], '\n')
		if end < 0 {
			end = len(body)
		} else {
			end += offset + 1
		}

		line := strings.TrimRight(string(body[offset:end]), "\r\n")
		if fence.consume(line) {
			previousStart = -1
			offset = end

			continue
		}

		level, text := parseHeading(line)
		if level > 0 {
			headings = append(headings, headingRange{level: level, text: text, start: offset})
			previousStart = -1
		} else if underline := setextLevel(line); underline > 0 && previousStart >= 0 {
			headings = append(headings, headingRange{level: underline, text: strings.TrimSpace(previous), start: previousStart})
			previousStart = -1
		} else {
			previous, previousStart = line, offset
			if strings.TrimSpace(line) == "" || indentedCode(line) {
				previousStart = -1
			}
		}

		offset = end
	}

	return headings
}

func indentedCode(line string) bool {
	columns := 0

	for _, character := range line {
		switch character {
		case ' ':
			columns++
		case '\t':
			columns += 4 - columns%4
		default:
			return columns >= 4
		}

		if columns >= 4 {
			return true
		}
	}

	return false
}

func setextLevel(line string) int {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return 0
	}

	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return 0
	}

	if strings.Trim(trimmed, "=") == "" {
		return 1
	}

	if strings.Trim(trimmed, "-") == "" {
		return 2
	}

	return 0
}

func headingPlain(text string) string {
	var out strings.Builder

	for position := 0; position < len(text); {
		character := text[position]
		if character == '\\' && position+1 < len(text) {
			out.WriteByte(text[position+1])
			position += 2

			continue
		}

		if value, consumed := headingCode(text[position:]); consumed > 0 {
			out.WriteString(value)

			position += consumed

			continue
		}

		if character == '<' {
			if end := strings.IndexByte(text[position:], '>'); end >= 0 {
				label := text[position+1 : position+end]
				if headingAutolink.MatchString(label) {
					out.WriteString(label)
				}

				position += end + 1

				continue
			}
		}

		if value, consumed := headingLink(text[position:]); consumed > 0 {
			out.WriteString(value)

			position += consumed

			continue
		}

		if character == '*' || character == '~' {
			position++
			continue
		}

		if character == '_' && emphasisUnderscore(text, position) {
			position++
			continue
		}

		out.WriteByte(character)

		position++
	}

	return html.UnescapeString(out.String())
}

func emphasisUnderscore(text string, position int) bool {
	previous, _ := utf8.DecodeLastRuneInString(text[:position])
	next, _ := utf8.DecodeRuneInString(text[position+1:])
	before := unicode.IsLetter(previous) || unicode.IsNumber(previous) || previous == '_'
	after := unicode.IsLetter(next) || unicode.IsNumber(next) || next == '_'

	return !before || !after
}

func balancedEnd(text string, start int, opening, closing byte) int {
	depth := 0

	for position := start; position < len(text); position++ {
		switch text[position] {
		case '\\':
			position++
		case opening:
			depth++
		case closing:
			depth--
			if depth == 0 {
				return position
			}
		}
	}

	return -1
}

func headingCode(text string) (string, int) {
	if text[0] != '`' {
		return "", 0
	}

	count := 1
	for count < len(text) && text[count] == '`' {
		count++
	}

	end := strings.Index(text[count:], strings.Repeat("`", count))
	if end < 0 {
		return "", 0
	}

	code := text[count : count+end]
	if strings.HasPrefix(code, " ") && strings.HasSuffix(code, " ") && strings.TrimSpace(code) != "" {
		code = code[1 : len(code)-1]
	}

	return code, count + end + count
}

func headingLink(text string) (string, int) {
	start := 0
	if text[0] == '!' {
		start++
	}

	if start >= len(text) || text[start] != '[' {
		return "", 0
	}

	closing := balancedEnd(text, start, '[', ']')
	if closing < 0 || closing+1 >= len(text) || text[closing+1] != '(' {
		return "", 0
	}

	end := balancedEnd(text, closing+1, '(', ')')
	if end < 0 {
		return "", 0
	}

	return headingPlain(text[start+1 : closing]), end + 1
}
