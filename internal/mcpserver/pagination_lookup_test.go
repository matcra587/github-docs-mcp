package mcpserver

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookupFragmentSelectsSection(t *testing.T) {
	t.Parallel()
	origin := newPaginationOrigin(t, 1, "## First\nfirst\n## Second\nsecond\n")
	session, _ := newSession(t, origin.fixture)
	result := callTool(t, session, toolGetDoc, map[string]any{"slug": "en/page-000#second"})
	require.False(t, result.IsError)
	require.Equal(t, "## Second\nsecond\n", pageBody(t, textOf(t, result)), "URL fragment must select only its section")
}

func TestLookupSelectorPrecedence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		slug    string
		heading string
		query   string
		want    string
		invalid bool
	}{
		{"fragment", "en/page-000#second", "", "", "## Second\nsecond\n", false},
		{"heading over fragment", "en/page-000#second", "First", "", "## First\nfirst\n", false},
		{"query over fragment", "en/page-000#missing", "", "first", "## First\nfirst\n", false},
		{"heading over both", "en/page-000#missing", "First", "second", "## First\nfirst\n", false},
		{"foreign with heading", "https://foreign.invalid/en/page-000#second", "First", "", "", true},
		{"fragment without path", "#second", "First", "second", "", true},
		{"malformed fragment with heading", "en/page-000#%ZZ", "First", "", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			origin := newPaginationOrigin(t, 1, "## First\nfirst\n## Second\nsecond\n")
			session, _ := newSession(t, origin.fixture)
			result := callTool(t, session, toolGetDoc, map[string]any{"slug": test.slug, "heading": test.heading, "query": test.query})
			require.Equal(t, test.invalid, result.IsError, textOf(t, result))

			if !test.invalid {
				require.Contains(t, pageBody(t, textOf(t, result)), test.want)
			}
		})
	}
}

func TestLookupAnchoredContinuation(t *testing.T) {
	t.Parallel()

	wanted := "## 環境\r\n" + strings.Repeat("設定を保持する\r\n", 10000)
	origin := newPaginationOrigin(t, 1, "## Before\nignore\n"+wanted+"## After\nignore\n")
	session, _ := newSession(t, origin.fixture)
	windows := traverse(t, session, toolGetDoc, map[string]any{"slug": "en/page-000#%E7%92%B0%E5%A2%83"})
	require.Greater(t, len(windows), 1)

	var actual strings.Builder

	for _, window := range windows {
		actual.WriteString(pageBody(t, window))
	}

	require.Equal(t, wanted, actual.String(), "anchored continuations preserve all original bytes")
}

func TestLookupMissingFragmentRejectsWholePage(t *testing.T) {
	t.Parallel()
	origin := newPaginationOrigin(t, 1, "## First\nfirst\n")
	session, _ := newSession(t, origin.fixture)
	result := callTool(t, session, toolGetDoc, map[string]any{"slug": "en/page-000#missing"})
	require.True(t, result.IsError, "unresolved fragment must not silently return the whole page")
}
