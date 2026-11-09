//go:build !unix

package main

import (
	"errors"
	"os"
)

func stopSelf() error {
	return errors.New("Ctrl-Z: no job control here")
}

// writeOutput: the selector's /tmp path only exists for the Unix editors
// that watch it.
func writeOutput(path, text string) error {
	return os.WriteFile(path, []byte(text), 0o600)
}
