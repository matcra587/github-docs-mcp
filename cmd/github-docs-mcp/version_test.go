package main

import (
	"runtime/debug"
	"testing"
)

func TestBuildVersion(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name, stamp, module, want string
		available                 bool
	}{
		{"release stamp", "1.2.3", "v1.0.0", "1.2.3", true},
		{"versioned install", "dev", "v1.0.0", "v1.0.0", true},
		{"empty stamp", "", "v1.0.0", "v1.0.0", true},
		{"pseudo version", "dev", "v0.0.0-20260101000000-123456789abc", "v0.0.0-20260101000000-123456789abc", true},
		{"local build", "dev", "(devel)", "dev", true},
		{"empty module", "dev", "", "dev", true},
		{"unavailable", "dev", "", "dev", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildVersion(tt.stamp, func() (*debug.BuildInfo, bool) {
				if tt.stamp != "" && tt.stamp != "dev" {
					t.Fatal("read build info despite release stamp")
				}

				return &debug.BuildInfo{Main: debug.Module{Version: tt.module}}, tt.available
			})
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
