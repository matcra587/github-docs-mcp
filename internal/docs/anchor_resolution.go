package docs

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"
)

type anchorHTMLKey struct{}

var (
	publishedHeadingPattern = regexp.MustCompile(`(?is)<h[1-6]\b([^>]*)>(.*?)</h[1-6]\s*>`)
	publishedIDPattern      = regexp.MustCompile(`\bid\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	publishedTagPattern     = regexp.MustCompile(`(?s)<[^>]*>`)
)

// ResolveHeading verifies published IDs before mapping them to source-byte selections.
func (s *Service) ResolveHeading(ctx context.Context, page Page, heading string) (string, error) {
	if !strings.HasPrefix(heading, "#") {
		return heading, nil
	}

	if _, err := ExtractHeading(page.Content, heading); err == nil {
		return heading, nil
	}

	target := strings.TrimPrefix(heading, "#")

	body, err := fetchShared(ctx, s, "anchor-html:"+page.Source.URL, func(shared context.Context) ([]byte, error) {
		return s.fetcher.Fetch(context.WithValue(shared, anchorHTMLKey{}, true), strings.TrimSuffix(page.Source.URL, ".md"))
	})
	if err != nil {
		return "", fmt.Errorf("cannot verify published heading anchor %q: %w", target, ErrHeadingNotFound)
	}

	return resolvePublishedHeading(page.Content, body, target)
}

func resolvePublishedHeading(markdown, body []byte, target string) (string, error) {
	text := ""

	for _, match := range publishedHeadingPattern.FindAllSubmatch(body, -1) {
		attribute := publishedIDPattern.FindSubmatch(match[1])
		if attribute == nil || html.UnescapeString(string(attribute[1])+string(attribute[2])) != target {
			continue
		}

		candidate := html.UnescapeString(publishedTagPattern.ReplaceAllString(string(match[2]), ""))
		if text != "" && text != candidate {
			return "", ErrHeadingNotFound
		}

		text = candidate
	}

	if text == "" {
		return "", ErrHeadingNotFound
	}

	resolved := ""

	anchors := make(map[string]bool)
	for _, candidate := range headingRanges(markdown) {
		anchor := uniqueAnchor(candidate.text, anchors)
		if headingPlain(candidate.text) != text {
			continue
		}

		if resolved != "" {
			return "", ErrHeadingNotFound
		}

		resolved = "#" + anchor
	}

	if resolved == "" {
		return "", ErrHeadingNotFound
	}

	return resolved, nil
}
