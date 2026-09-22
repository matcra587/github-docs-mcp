package docs

import (
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
	headings := headingRanges(md)
	sections := make([]Section, 0, len(headings))
	crumb := make([]string, 7)

	for index, heading := range headings {
		end := len(md)
		if index+1 < len(headings) {
			end = headings[index+1].start
		}

		crumb[heading.level] = heading.text
		for level := heading.level + 1; level < len(crumb); level++ {
			crumb[level] = ""
		}

		sections = append(sections, Section{Level: heading.level, Heading: heading.text, Breadcrumb: breadcrumb(crumb, heading.level), Body: md[heading.start:end]})
	}

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
