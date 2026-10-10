//go:build windows

package main

import _ "embed"

// configTemplate is what a config file the Settings tab makes starts from:
// the example file, so that a new file explains itself the same way.
//
//go:embed config.example.yaml
var configTemplate []byte
