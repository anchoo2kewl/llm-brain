package main

import "os"

func install(download, target, next string) error { return os.Rename(download, target) }
