package docs

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// SupportedLanguages returns GitHub Docs' supported language codes. The live
// canary compares this list with /api/pagelist/languages to detect upstream drift.
func SupportedLanguages() []string {
	return []string{"en", "es", "ja", "pt", "zh", "ru", "fr", "ko", "de"}
}

// Language returns the language of this catalogue view.
func (s *Service) Language() string {
	if s.language == "" {
		return docsLanguage
	}

	return s.language
}

// ForLanguage selects a catalogue without changing any other caller's language.
// Views share the HTTP client, page cache and disk budget. Their catalogues,
// refresh flights and failure cooldowns are independent.
func (s *Service) ForLanguage(language string) (*Service, error) {
	if language == "" {
		language = docsLanguage
	}

	if language == s.Language() {
		return s, nil
	}

	if view, ok := s.languages[language]; ok {
		return view, nil
	}

	return nil, fmt.Errorf("%w %q; supported: %s", ErrUnsupportedLanguage, language, strings.Join(SupportedLanguages(), ", "))
}

// languageViews creates only small state holders; catalogues are fetched on
// demand. The map is immutable after construction, so concurrent selection
// needs no lock and cannot allocate unbounded state from arbitrary input.
func (s *Service) languageViews() {
	s.languages = map[string]*Service{docsLanguage: s}

	for _, language := range SupportedLanguages()[1:] {
		s.languages[language] = &Service{
			language: language, languages: s.languages,
			now: s.now, fetcher: s.fetcher, baseURL: s.baseURL,
			pages: s.pages, disk: s.disk, indexTTL: s.indexTTL, pageTTL: s.pageTTL,
		}
	}
}

func (s *Service) forSlug(slug string) (*Service, error) {
	language := SlugLanguage(slug)
	return s.ForLanguage(language)
}

// SlugLanguage reads a language prefix from a slug or documentation URL.
// Prefixes are validated when selecting the corresponding service view.
func SlugLanguage(slug string) string {
	language, _, ok := strings.Cut(normalizeSlug(slug), "/")
	if !ok {
		language = docsLanguage
	}

	return language
}

func languagePath(path, basePath string) string {
	language, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(path, strings.TrimSuffix(basePath, "/")), "/"), "/")
	if slices.Contains(SupportedLanguages(), language) {
		return language
	}

	return ""
}

func requestLanguage(u *url.URL, basePath string) string {
	path := strings.TrimPrefix(u.Path, strings.TrimSuffix(basePath, "/"))
	if path == searchPath {
		return u.Query().Get("language")
	}

	if rest, ok := strings.CutPrefix(path, pageListPath+"/"); ok {
		language, _, _ := strings.Cut(rest, "/")
		if slices.Contains(SupportedLanguages(), language) {
			return language
		}
	}

	return languagePath(u.Path, basePath)
}

func contentLanguageMatches(header, language string) bool {
	if header == "" {
		return true
	}

	for tag := range strings.SplitSeq(header, ",") {
		primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		if primary == language {
			return true
		}
	}

	return false
}

// EnglishAlternative returns a known English catalogue entry for a missing
// translation. It never fetches or returns the English page body.
func (s *Service) EnglishAlternative(ctx context.Context, slug string) string {
	language, path, ok := strings.Cut(normalizeSlug(slug), "/")
	if !ok || language == docsLanguage || !slices.Contains(SupportedLanguages(), language) {
		return ""
	}

	english, err := s.ForLanguage(docsLanguage)
	if err != nil {
		return ""
	}

	idx, err := english.index(ctx)
	if err != nil {
		return ""
	}

	alternative := docsLanguage + "/" + path
	if _, ok := idx.BySlug(alternative); ok {
		return alternative
	}

	return ""
}

func (s *Service) catalogueKey(key string) string {
	if s.Language() == docsLanguage {
		return key // Preserve the existing English disk cache.
	}

	return key + ":" + s.Language() + ":" + docsVersion
}

func catalogueKeyLanguage(key string) (language, component string, ok bool) {
	decoded, err := url.PathUnescape(key)
	if err != nil {
		return "", "", false
	}

	if decoded == diskIndexKey || decoded == diskPageListKey {
		return docsLanguage, decoded, true
	}

	parts := strings.Split(decoded, ":")
	if len(parts) != 3 || parts[0] != diskPageListKey || parts[2] != docsVersion || !slices.Contains(SupportedLanguages(), parts[1]) {
		return "", "", false
	}

	return parts[1], diskPageListKey, true
}
