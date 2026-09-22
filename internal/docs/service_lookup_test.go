package docs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupRejectsForeignAuthority(t *testing.T) {
	t.Parallel()
	origin := newFixtureOrigin(t)
	service := newTestService(t, origin)
	_, err := service.Get(t.Context(), "https://foreign.invalid/en/alpha")
	assert.Zero(t, origin.hitCount("/en/alpha.md"), "rejected authority must not fetch the local page")
	require.Error(t, err, "foreign authority with a known path must be rejected")
}

func TestLookupExactDuplicateAnchor(t *testing.T) {
	t.Parallel()

	body := []byte("## Setup\nfirst\n## Setup\nsecond\n")
	section, err := ExtractHeading(body, "#setup-1")
	require.NoError(t, err, "generated duplicate anchor must select the second heading")
	assert.Equal(t, "## Setup\nsecond\n", string(section))
}

func TestLookupInlineLinkAnchor(t *testing.T) {
	t.Parallel()

	section, err := ExtractHeading([]byte("## Read [the guide](/en/guide)\nbody\n"), "#read-the-guide")
	require.NoError(t, err, "link destinations must not become heading anchor text")
	assert.Equal(t, "## Read [the guide](/en/guide)\nbody\n", string(section))
}

func TestLookupPunctuationAnchor(t *testing.T) {
	t.Parallel()

	section, err := ExtractHeading([]byte("## `jobs.<job_id>`\nbody\n"), "#jobsjob_id")
	require.NoError(t, err, "GitHub anchor removes punctuation but preserves underscores")
	assert.Equal(t, "## `jobs.<job_id>`\nbody\n", string(section))
}

func TestLookupFenceLength(t *testing.T) {
	t.Parallel()

	body := []byte("## Real\n````sh\n```\n## Fake\n````\n## End\nend\n")
	_, err := ExtractHeading(body, "#fake")
	require.ErrorIs(t, err, ErrHeadingNotFound, "shorter fence must not close the enclosing block")
}
