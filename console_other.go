//go:build !windows

package main

// ownConsole reports whether tagshelf was started by a double click, which
// only Windows gives a console window of its own.
var ownConsole = func() bool { return false }

// waitForEnter keeps a window that would close with the program open.
var waitForEnter = func() {}
