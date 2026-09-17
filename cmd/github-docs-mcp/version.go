package main

import "runtime/debug"

// buildVersion preserves release stamps and recognises versioned go installs.
func buildVersion(stamped string, readInfo func() (*debug.BuildInfo, bool)) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}

	info, ok := readInfo()
	if !ok || info == nil {
		return "dev"
	}

	if info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "dev"
	}

	return info.Main.Version
}
