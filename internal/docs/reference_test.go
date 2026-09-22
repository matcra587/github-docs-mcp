package docs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReferenceScope(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"en/alpha", "/docs/en/alpha", "https://docs.github.com/docs/en/alpha.md/", "https://DOCS.GITHUB.COM:443/docs/en/alpha", "en/%61lpha#%E7%92%B0%E5%A2%83", "en/alpha#", "en/alpha#%2520"} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			service := NewService(nil, "https://docs.github.com/docs", ServiceConfig{})
			reference, err := service.ParseReference(input)
			require.NoError(t, err)
			assert.Equal(t, "en/alpha", reference.Path)

			if input == "en/alpha#%2520" {
				assert.Equal(t, "%20", reference.Fragment, "fragment decoding happens once")
			}
		})
	}

	for _, input := range []string{"", "#anchor", "//docs.github.com/docs/en/alpha", "https://docs.github.com.evil.invalid/docs/en/alpha", "https://user@docs.github.com/docs/en/alpha", "http://docs.github.com/docs/en/alpha", "https://docs.github.com:444/docs/en/alpha", "/en/alpha", "/docs/../en/alpha", "en/%2e%2e/alpha", "en%2falpha", "en/%5calpha", "en/%ZZ", "en/alpha#%ZZ", "en/alpha?query=x", "en/enterprise-server@3.18/alpha", "it/alpha"} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			service := NewService(nil, "https://docs.github.com/docs", ServiceConfig{})
			_, err := service.ParseReference(input)
			require.Error(t, err)
		})
	}
}

func FuzzReference(f *testing.F) {
	f.Add("en/alpha#setup")
	f.Add("https://docs.github.com/en/alpha")

	service := NewService(nil, "https://docs.github.com", ServiceConfig{})

	f.Fuzz(func(t *testing.T, input string) {
		parsed, err := service.ParseReference(input)
		if err == nil {
			require.NotEmpty(t, parsed.Path)
			require.NoError(t, validateReferencePath(parsed.Path))
		}
	})
}
