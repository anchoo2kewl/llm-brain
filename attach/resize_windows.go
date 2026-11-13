package main

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// prepareConsole lets the console interpret the escape sequences tmux sends.
func prepareConsole() {
	h := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) == nil {
		_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING|windows.DISABLE_NEWLINE_AUTO_RETURN)
	}
}

// watchResize polls the console size: Windows has no SIGWINCH.
func watchResize() <-chan struct{} {
	out := make(chan struct{}, 1)
	go func() {
		c, r := size()
		for range time.Tick(250 * time.Millisecond) {
			if nc, nr := size(); nc != c || nr != r {
				c, r = nc, nr
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}
