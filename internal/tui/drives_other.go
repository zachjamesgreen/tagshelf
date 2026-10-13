//go:build !windows

package tui

// drives lists nothing off Windows: everything is under /.
func drives() []string { return nil }
