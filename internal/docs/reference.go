package docs

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

func invalidReference(reason string) error {
	return fmt.Errorf("unsupported documentation reference: %s", reason)
}

// ParseReference validates documentation scope before exposing a slug and decoded fragment.
func (s *Service) ParseReference(input string) (url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(input))
	if err != nil {
		return url.URL{}, invalidReference("malformed URL or escape")
	}

	base, err := url.Parse(s.baseURL)
	if err != nil {
		return url.URL{}, invalidReference("invalid configured origin")
	}

	if err := validateReferenceURL(parsed, base); err != nil {
		return url.URL{}, err
	}

	path := parsed.Path
	if strings.HasPrefix(path, "/") {
		prefix := strings.TrimSuffix(base.Path, "/") + "/"
		if !strings.HasPrefix(path, prefix) {
			return url.URL{}, invalidReference("path is outside the configured base path")
		}

		path = strings.TrimPrefix(path, prefix)
	}

	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".md")
	if err := validateReferencePath(path); err != nil {
		return url.URL{}, err
	}

	language, _, _ := strings.Cut(path, "/")
	if _, err := s.ForLanguage(language); err != nil {
		return url.URL{}, err
	}

	return url.URL{Path: path, Fragment: parsed.Fragment}, nil
}

func validateReferenceURL(parsed, base *url.URL) error {
	if parsed.User != nil || parsed.Opaque != "" || (parsed.Host != "" && parsed.Scheme == "") {
		return invalidReference("userinfo, opaque and protocol-relative URLs are not supported")
	}

	if parsed.Scheme != "" && !sameReferenceOrigin(parsed, base) {
		return invalidReference("URL must use the configured origin and scheme")
	}

	if parsed.RawQuery != "" || parsed.ForceQuery {
		return invalidReference("query strings are not supported; remove the query string")
	}

	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || !utf8.ValidString(parsed.Fragment) {
		return invalidReference("invalid encoding or encoded path separator")
	}

	return nil
}

func sameReferenceOrigin(address, base *url.URL) bool {
	return strings.EqualFold(address.Scheme, base.Scheme) && strings.EqualFold(address.Hostname(), base.Hostname()) && effectivePort(address) == effectivePort(base)
}

func validateReferencePath(path string) error {
	if strings.ContainsAny(path, "\\%?#") || !utf8.ValidString(path) || strings.ContainsFunc(path, unicode.IsControl) {
		return invalidReference("invalid path characters or encoding")
	}

	for component := range strings.SplitSeq(path, "/") {
		if component == "" || component == "." || component == ".." {
			return invalidReference("empty or dot path segments are not supported")
		}
	}

	_, rest, ok := strings.Cut(path, "/")
	if !ok || rest == "" {
		return invalidReference("a language and page path are required before an anchor")
	}

	product, _, _ := strings.Cut(rest, "/")
	if strings.Contains(product, "@") || strings.HasPrefix(product, "enterprise-") {
		return invalidReference("only current GitHub.com documentation is supported, not enterprise or version paths")
	}

	return nil
}

func effectivePort(address *url.URL) string {
	if port := address.Port(); port != "" {
		return port
	}

	if strings.EqualFold(address.Scheme, "https") {
		return "443"
	}

	return "80"
}
