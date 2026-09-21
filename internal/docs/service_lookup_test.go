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
