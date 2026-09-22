package docs

import (
	"net/url"
	"strings"
)

func validateRedirectLocation(location string) error {
	parsed, err := url.Parse(location)
	if err != nil {
		return err
	}

	for part := range strings.SplitSeq(parsed.Path, "/") {
		if part == "." || part == ".." {
			return invalidReference("redirect contains dot segments")
		}
	}

	return nil
}

func validateFetchScope(address, base *url.URL) error {
	if !sameReferenceOrigin(address, base) || address.User != nil {
		return invalidReference("request outside base origin")
	}

	prefix := strings.TrimSuffix(base.Path, "/") + "/"
	if !strings.HasPrefix(address.Path, prefix) {
		return invalidReference("request leaves configured base path")
	}

	path := strings.TrimPrefix(address.Path, prefix)
	for component := range strings.SplitSeq(path, "/") {
		if component == "." || component == ".." || strings.ContainsAny(component, "\\") {
			return invalidReference("request contains unsafe path segments")
		}
	}

	escaped := strings.ToLower(address.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return invalidReference("request contains encoded separators")
	}

	if languagePath(address.Path, base.Path) != "" {
		if err := validateReferencePath(path); err != nil {
			return err
		}
	}

	return nil
}
