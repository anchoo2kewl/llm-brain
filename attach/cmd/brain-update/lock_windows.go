package main

// lockDir: shortcut: no folder lock on Windows; the app runs one check at a time and the final rename replaces
// the setup file in one step. Add a lock file if a second process can start the helper.
func lockDir(string) (func(), error) { return func() {}, nil }
