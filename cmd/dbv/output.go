package main

import "syscall"

// stopSelf is Ctrl-Z: SIGTSTP to the process group, as a shell's job
// control expects; it returns once the job is continued (fg).
func stopSelf() error {
	return syscall.Kill(0, syscall.SIGTSTP)
}
