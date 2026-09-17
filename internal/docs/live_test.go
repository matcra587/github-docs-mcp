//go:build integration

package docs

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live drift canaries against the real docs origin. Scheduled CI only, because an
// origin change must show up as a red nightly run, never a blocked PR.

const liveBaseURL = "https://docs.github.com"

// liveSlug is a page deep enough in the tree that it can only come from the
// page list, not the curated catalogue.
const liveSlug = "en/actions/tutorials/build-and-test-code/nodejs"

func liveService(t *testing.T) *Service {
	t.Helper()

	client, err := NewClient(liveBaseURL)
	require.NoError(t, err)

	return NewService(client, liveBaseURL, ServiceConfig{
		IndexTTL:      time.Hour,
		PageTTL:       time.Hour,
		CacheMaxBytes: 64 << 20,
	})
}

func TestLiveIndexParses(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	svc := liveService(t)

	docs, err := svc.List(t.Context(), "", 0)
	must.NoError(err, "live llms.txt failed to fetch/parse: format drift?")

	// The curated catalogue alone is ~120 entries; the page list widens it to
	// thousands. A count near the low end means the merge silently lost the
	// page list, exactly the drift worth failing on.
	is.Greater(len(docs), 1000, "live catalogue suspiciously small: page-list merge broken?")
}

func TestLiveCatalogueCarriesBothSources(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	svc := liveService(t)

	docs, err := svc.List(t.Context(), "", 0)
	must.NoError(err)

	var curated, derived int

	for _, d := range docs {
		if d.Description != "" {
			curated++
		} else {
			derived++
		}
	}

	is.Positive(curated, "no entry carried a description: llms.txt prose lost")
	is.Positive(derived, "no page-list-only entry survived the merge")
}

func TestLivePageFetches(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	svc := liveService(t)

	page, err := svc.Get(t.Context(), liveSlug)
	must.NoError(err, "live page fetch failed")

	must.GreaterOrEqual(len(page.Content), 1000, "live page suspiciously small")

	// The origin serves rendered HTML at the bare path and markdown only at
	// the .md form; an HTML body here means the URL construction drifted.
	is.NotContains(strings.ToLower(string(page.Content[:200])), "<!doctype", "expected markdown, got HTML")
}

func TestLiveSearchReturnsHits(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	svc := liveService(t)

	hits, err := svc.Search(t.Context(), "cache action dependencies", 5)
	must.NoError(err, "live search failed")

	must.NotEmpty(hits, "live search returned nothing: endpoint or client_name drift?")

	is.NotEmpty(hits[0].Doc.Slug, "hit carried no slug")
	is.NotEmpty(hits[0].Snippet, "hit carried no snippet: highlights schema drift?")

	// Every hit must be addressable by get_doc, or search sends callers to
	// slugs the catalogue denies.
	_, err = svc.Get(t.Context(), hits[0].Doc.Slug)
	is.NoError(err, "top search hit %q is not fetchable", hits[0].Doc.Slug)
}

func TestLiveSearchHonoursLimit(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	svc := liveService(t)

	// Search's limit contract is enforced by the endpoint's size parameter,
	// not locally, so an origin that started ignoring size would silently
	// uncap every search. "actions" matches over a thousand pages.
	hits, err := svc.Search(t.Context(), "actions", 3)
	must.NoError(err)

	is.Len(hits, 3, "origin ignored the size parameter")

	uncapped, err := svc.Search(t.Context(), "actions", 0)
	must.NoError(err)

	is.Greater(len(uncapped), len(hits), "limit<=0 must mean no cap, not one result")
	is.LessOrEqual(len(uncapped), searchAPIMax, "no cap is still bounded by the endpoint's page size")
}
