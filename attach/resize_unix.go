//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func prepareConsole() {}

// watchResize reports SIGWINCH.
func watchResize() <-chan struct{} {
	sig := make(chan os.Signal, 1)
	out := make(chan struct{}, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	go func() {
		for range sig {
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out
}
