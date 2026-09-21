package docs

import (
	"bufio"
	"bytes"
	"sort"
	"strings"
)

const phraseBoost = 5

// Section is one heading-delimited slice of a page: the heading text, its
// breadcrumb of ancestor headings, and the verbatim body (heading line
// included) up to the next same-or-higher-level heading.
type Section struct {
	Level      int
	Heading    string
	Breadcrumb string
	Body       []byte
}

// SplitSections parses md into sections by ATX heading, fence-aware so a '#'
// comment inside a code block never starts a section. Content before the first
// heading is dropped (it belongs to no section).
func SplitSections(md []byte) []Section {
	var (
		sections []Section
		cur      *Section
		curBody  bytes.Buffer
		crumb    []string // heading text per level index (1-based, [0] unused)
		inFence  bool
	)

	crumb = make([]string, 7)

	flush := func() {
		if cur != nil {
			cur.Body = append([]byte(nil), curBody.Bytes()...)
			sections = append(sections, *cur)
		}

		curBody.Reset()
	}

	sc := bufio.NewScanner(bytes.NewReader(md))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sc.Split(scanRawLines)

	for sc.Scan() {
		raw := sc.Text()
		line := strings.TrimRight(raw, "\r\n")

		if isFenceDelimiter(line) {
			inFence = !inFence

			if cur != nil {
				curBody.WriteString(raw)
			}

			continue
		}

		level, text := 0, ""
		if !inFence {
			level, text = parseHeading(line)
		}

		if level == 0 {
			if cur != nil {
				curBody.WriteString(raw)
			}

			continue
		}

		// New heading: close the previous section, update the breadcrumb.
		flush()

		crumb[level] = text
		for i := level + 1; i < len(crumb); i++ {
			crumb[i] = ""
		}

		cur = &Section{Level: level, Heading: text, Breadcrumb: breadcrumb(crumb, level)}

		curBody.WriteString(raw)
	}

	flush()

	return sections
}

func breadcrumb(crumb []string, level int) string {
	var parts []string

	for i := 1; i <= level; i++ {
		if crumb[i] != "" {
			parts = append(parts, crumb[i])
		}
	}

	return strings.Join(parts, " > ")
}

// SectionHit is a section matched by SearchSections.
type SectionHit struct {
	Section

	Score int
}

// SearchSections ranks sections against a whitespace-tokenised query. A token
// matching the heading scores higher than one matching only the body; every
// token must match somewhere. limit <= 0 means no cap.
func SearchSections(secs []Section, query string, limit int) []SectionHit {
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return nil
	}

	phrase := strings.ToLower(strings.TrimSpace(query))
	hits := []SectionHit{}

	for _, s := range secs {
		heading := strings.ToLower(s.Heading + " " + s.Breadcrumb)
		body := strings.ToLower(string(s.Body))

		score, ok := 0, true

		for _, tok := range tokens {
			switch {
			case strings.Contains(heading, tok):
				score += titleWeight
			case strings.Contains(body, tok):
				score += bodyWeight
			default:
				ok = false
			}

			if !ok {
				break
			}
		}

		if !ok {
			continue
		}

		// A section whose heading is (or contains) the exact query is the most
		// specific match, so boost it so "exit code 2" beats "exit codes".
		if strings.Contains(heading, phrase) {
			score += phraseBoost
		}

		hits = append(hits, SectionHit{Section: s, Score: score})
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })

	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}

	return hits
}
