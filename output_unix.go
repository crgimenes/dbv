//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// writeOutput replaces the selector's file in /tmp, a directory everyone
// writes to: a symlink planted there is refused (O_NOFOLLOW), and so is a
// file someone else owns, before anything is truncated.
func writeOutput(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- the fixed selector path
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not a file of yours: not written", path)
	}
	err = f.Chmod(0o600)
	if err != nil {
		return err
	}
	err = f.Truncate(0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	return err
}
