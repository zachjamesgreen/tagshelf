//go:build windows

package main

import "os"

// stopSignals are what stops a run the gentle way: Ctrl-C.
var stopSignals = []os.Signal{os.Interrupt}

// ownTempFolder does nothing on Windows, where the temp folder is already
// the user's own.
func ownTempFolder() {}
