package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVersionComparison(t *testing.T) {
	old, _ := version("0.1.9")
	next, ok := version("0.1.10")
	if !ok || !newer(next, old) || newer(old, next) || newer(old, old) {
		t.Fatal("numeric version ordering")
	}
	for _, s := range []string{"0.1", "0.1.1-beta", "0.01.1", "0.1.-1", "client-v0.1.1"} {
		if _, ok := version(s); ok {
			t.Fatal(s)
		}
	}
}

func TestLatestSkipsDraftsAndPrereleases(t *testing.T) {
	running, _ := version("0.1.12")
	releases := []release{
		{Tag: "client-v0.1.15", Draft: true},
		{Tag: "client-v0.1.14", Prerelease: true},
		{Tag: "hub-v0.2.0"},
		{Tag: "client-v0.1.13"},
		{Tag: "client-v0.1.11"},
		{Tag: "client-v0.1.x"},
	}
	if r, ok := latest(releases, running); !ok || r.Tag != "client-v0.1.13" {
		t.Fatalf("got %q %v", r.Tag, ok)
	}
	// Only the draft is newer (today's state while the release workflow runs): nothing to do.
	if r, ok := latest(releases[:3], running); ok {
		t.Fatalf("draft or prerelease chosen: %q", r.Tag)
	}
	newest, _ := version("0.1.13")
	if _, ok := latest(releases, newest); ok {
		t.Fatal("same version is not an update")
	}
}

func TestAssetName(t *testing.T) {
	for _, c := range [][4]string{
		{"darwin", "arm64", "BrainHub-mac-0.1.13.zip"},
		{"linux", "amd64", "Brain Hub_0.1.13_amd64.AppImage"},
		{"windows", "amd64", "Brain Hub_0.1.13_x64-setup.exe"},
	} {
		if got, err := assetName(c[0], c[1], "0.1.13"); err != nil || got != c[2] {
			t.Fatalf("%s: %q %v", c[0], got, err)
		}
	}
	if _, err := assetName("linux", "arm64", "0.1.13"); err == nil {
		t.Fatal("no arm64 Linux build is published")
	}
}

func TestTargetOK(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Brain Hub setup.exe")
	missing, missingErr := os.Lstat(file)
	if !targetOK("windows", file, missing, missingErr) {
		t.Fatal("a Windows setup path may not exist yet")
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(file)
	if !targetOK("windows", file, st, err) || !targetOK("linux", file, st, err) {
		t.Fatal("regular file")
	}
	dirSt, dirErr := os.Lstat(dir)
	if targetOK("windows", dir, dirSt, dirErr) || targetOK("darwin", dir, dirSt, dirErr) || targetOK("linux", dir, dirSt, dirErr) {
		t.Fatal("a plain folder is no install location")
	}
	if targetOK("linux", file+".missing", missing, missingErr) {
		t.Fatal("a missing AppImage")
	}
}
