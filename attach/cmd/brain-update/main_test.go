//go:build darwin || linux

package main

import "testing"

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
