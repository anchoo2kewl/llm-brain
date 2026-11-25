//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockDir stops two updaters from writing into one folder at the same time.
func lockDir(dir string) (func(), error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
