package main

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os/exec"
	"path/filepath"
	"strings"
)

func install(download, target, next string) error {
	dir := filepath.Dir(download)
	if err := exec.Command("/usr/bin/ditto", "-x", "-k", download, dir).Run(); err != nil {
		return err
	}
	bundle := filepath.Join(dir, "Brain Hub.app")
	plist := filepath.Join(bundle, "Contents", "Info.plist")
	for key, want := range map[string]string{"CFBundleIdentifier": "app.llmbrain.hub", "CFBundleShortVersionString": next} {
		out, err := exec.Command("/usr/bin/plutil", "-extract", key, "raw", "-o", "-", plist).Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			return fmt.Errorf("invalid release bundle")
		}
	}
	if err := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", bundle).Run(); err != nil {
		return err
	}
	return unix.RenamexNp(bundle, target, unix.RENAME_SWAP)
}
