package docs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSearchSizesDoNotFetchPages(t *testing.T) {
	t.Parallel()
	f := newFixtureOrigin(t)
	s := newTestService(t, f)
	check := func(known bool) {
		t.Helper()
		hits, err := s.Search(t.Context(), "alpha", 10)
		require.NoError(t, err)
		require.NotEmpty(t, hits)

		for _, hit := range hits {
			if hit.Doc.Slug != "en/alpha" {
				continue
			}

			if known {
				require.NotNil(t, hit.PageBytes)
				require.Equal(t, len(fixturePages["en/alpha"]), *hit.PageBytes)
			} else {
				require.Nil(t, hit.PageBytes)
			}

			return
		}

		t.Fatal("missing alpha result")
	}
	check(false)
	require.Zero(t, f.hitCount("/en/alpha.md"))
	_, err := s.Get(t.Context(), "en/alpha")
	require.NoError(t, err)
	check(true)
	require.Equal(t, 1, f.hitCount("/en/alpha.md"))
	s.pages.putAged("en/alpha", []byte(fixturePages["en/alpha"]), time.Hour, time.Now().Add(-2*time.Hour))
	f.failing.Store(true)
	check(true)
	require.Equal(t, 1, f.hitCount("/en/alpha.md"), "stale size hints must not fetch")
}
