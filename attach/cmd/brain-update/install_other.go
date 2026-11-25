//go:build !darwin

package main

import "os"

// install moves the verified file into place: the AppImage, or the Windows setup that the restart runs.
func install(download, target, next string) error { return os.Rename(download, target) }
