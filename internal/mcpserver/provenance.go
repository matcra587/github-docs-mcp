package mcpserver

import (
	"fmt"
	"strings"
	"time"

	"github.com/matcra587/github-docs-mcp/internal/docs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func sourceText(source docs.Source) string {
	if source.URL == "" {
		return ""
	}

	at := "unknown"

	if source.Product == "" {
		source.Product = "all/unspecified"
	}

	if !source.FetchedAt.IsZero() {
		at = source.FetchedAt.UTC().Format(time.RFC3339Nano)
	}

	return fmt.Sprintf("Source: %s | language: %s | product: %s | version: %s | Fetched: %s | freshness: %s\n",
		source.URL, source.Language, source.Product, source.Version, at, source.Freshness)
}

func coverageText(coverage docs.Coverage) string {
	var b strings.Builder
	if coverage.Mode != "" {
		fmt.Fprintf(&b, "Search: %s. Page freshness is unknown unless a cached body supplied the match.\n", coverage.Mode)
	}

	if coverage.Degraded {
		b.WriteString("Coverage: degraded; missing or stale catalogue sources, or reduced search coverage.\n")
	}

	if coverage.StaleBodies {
		b.WriteString("Search dependencies include stale cached page bodies.\n")
	}

	for _, source := range coverage.Sources {
		b.WriteString(sourceText(source))
	}

	return b.String()
}

func withProvenance(result *mcp.CallToolResult, source docs.Source, coverage docs.Coverage) *mcp.CallToolResult {
	if len(result.Content) > 0 {
		if text, ok := result.Content[0].(*mcp.TextContent); ok {
			text.Text = sourceText(source) + coverageText(coverage) + "\n" + text.Text
		}
	}

	return result
}
