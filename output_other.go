//go:build !unix

package main

import "os"

// writeOutput: the selector's /tmp path only exists for the Unix editors
// that watch it.
func writeOutput(path, text string) error {
	return os.WriteFile(path, []byte(text), 0o600)
}
