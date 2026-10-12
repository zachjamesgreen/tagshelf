package tui

import "github.com/zachjamesgreen/tagshelf/internal/fixtures"

// p gives a path the tests write the Windows way, or a text with such paths
// in it, the form it has on this system: D:\Music\A is /d/Music/A off
// Windows. See fixtures.OSPath.
func p(s string) string { return fixtures.OSPath(s) }
