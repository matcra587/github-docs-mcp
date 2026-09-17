package docs

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxContentBytes caps content returned to clients: token thrift, distinct
// from the transport-level maxBodyBytes memory cap.
const maxContentBytes = 50 * 1024

// ExtractHeading returns the section of md starting at the heading whose text
// equals heading (case-insensitive, any level) and ending before the next
// heading of the same or higher level.
func ExtractHeading(md []byte, heading string) ([]byte, error) {
	want := strings.ToLower(strings.TrimSpace(heading))
	// Agents often lift the kebab-case anchor from a page link (#hook-events)
	// rather than the heading prose. Accept either form.
	wantSlug := slugifyHeading(strings.TrimPrefix(want, "#"))

	var (
		out       bytes.Buffer
		inSection bool
		inFence   bool
		level     int
	)

	sc := bufio.NewScanner(bytes.NewReader(md))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	emit := func(line string) {
		if inSection {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}

	for sc.Scan() {
		line := sc.Text()

		// Shell comments inside fenced code blocks start with '#' too; while
		// a fence is open, nothing is a heading.
		if isFenceDelimiter(line) {
			inFence = !inFence

			emit(line)

			continue
		}

		if inFence {
			emit(line)
			continue
		}

		hLevel, hText := parseHeading(line)
		if inSection && hLevel > 0 && hLevel <= level {
			break
		}

		if !inSection && hLevel > 0 && headingMatches(hText, want, wantSlug) {
			inSection = true
			level = hLevel
		}

		emit(line)
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan page: %w", err)
	}

	if !inSection {
		return nil, fmt.Errorf("heading %q: %w", heading, ErrHeadingNotFound)
	}

	return out.Bytes(), nil
}

// isFenceDelimiter reports whether line opens or closes a fenced code block.
// CommonMark allows up to three spaces of indentation before the fence.
func isFenceDelimiter(line string) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return false
	}

	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// headingMatches reports whether a heading's text equals the wanted literal
// text (case-insensitive) or its kebab-case anchor slug.
func headingMatches(hText, want, wantSlug string) bool {
	if strings.ToLower(hText) == want {
		return true
	}

	return wantSlug != "" && slugifyHeading(hText) == wantSlug
}

// slugifyHeading converts heading prose to its anchor form: lowercase, runs of
// non-alphanumerics collapse to a single hyphen, trimmed.
func slugifyHeading(h string) string {
	var b strings.Builder

	lastHyphen := true // avoids a leading hyphen

	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)

			lastHyphen = false
		case !lastHyphen:
			b.WriteByte('-')

			lastHyphen = true
		}
	}

	return strings.Trim(b.String(), "-")
}

// PageHeadings returns the heading texts of a page, for near-miss suggestions.
func PageHeadings(md []byte) []string {
	secs := SplitSections(md)
	out := make([]string, 0, len(secs))

	for _, s := range secs {
		out = append(out, s.Heading)
	}

	return out
}

// parseHeading returns the ATX heading level and text of line, or 0 when the
// line is not a heading.
func parseHeading(line string) (int, string) {
	trimmed := strings.TrimLeft(line, "#")

	level := len(line) - len(trimmed)
	if level == 0 || level > 6 || !strings.HasPrefix(trimmed, " ") {
		return 0, ""
	}

	return level, strings.TrimSpace(trimmed)
}

// Window is one pageful of content: bytes [Offset, Next) of a Total-byte
// document. Truncated means more content exists past Next.
type Window struct {
	Content []byte
	Offset  int
	Next    int
	Total   int
}

// Truncated reports whether content continues past this window.
func (w Window) Truncated() bool { return w.Next < w.Total }

// Paginate returns the window of content starting at offset, capped at
// maxContentBytes and cut on a line boundary where possible. A negative
// offset is treated as 0; an offset at or past the end returns an empty,
// non-truncated window (Next == Total) so callers can report the overshoot.
func Paginate(content []byte, offset int) Window {
	total := len(content)
	offset = max(offset, 0)

	if offset >= total {
		return Window{Offset: offset, Next: total, Total: total}
	}

	rest := content[offset:]
	if len(rest) <= maxContentBytes {
		return Window{Content: rest, Offset: offset, Next: total, Total: total}
	}

	cut := rest[:maxContentBytes]
	if i := bytes.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	} else {
		// No line boundary in range (e.g. one giant table row): back off to
		// a rune boundary so the window is valid UTF-8. Without this the
		// JSON transport replaces the split rune's bytes with U+FFFD in both
		// this window and the next, breaking offset-based reassembly.
		cut = trimPartialRune(cut)
	}

	return Window{Content: cut, Offset: offset, Next: offset + len(cut), Total: total}
}

// trimPartialRune drops any trailing bytes of b that form an incomplete UTF-8
// encoding, so b ends on a rune boundary.
func trimPartialRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i > len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if r, _ := utf8.DecodeRune(b[i:]); r == utf8.RuneError {
				return b[:i]
			}

			return b
		}
	}

	return b
}
