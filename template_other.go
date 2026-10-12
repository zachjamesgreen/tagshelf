//go:build !windows

package main

import _ "embed"

// configTemplate is what a config file the Settings tab makes starts from:
// the example file, so that a new file explains itself the same way. It is
// the example whose paths are written as they are off Windows.
//
//go:embed config.example.linux.yaml
var configTemplate []byte
