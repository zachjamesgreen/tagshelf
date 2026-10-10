// Package version says which tagshelf this is.
package version

import "runtime/debug"

// release is the version a release build is given, with
//
//	-ldflags "-X github.com/zachjamesgreen/tagshelf/internal/version.release=v0.1.0"
//
// It is empty in every other build.
var release string

// String is the release version, or else the module version from the build
// info (what go install records), or "dev" for a build without one (go run,
// go test, a plain go build of a checkout).
func String() string {
	if release != "" {
		return release
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}
