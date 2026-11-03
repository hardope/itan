//go:build !darwin && !windows

package main

import (
	"errors"
	"log"
	"os/exec"
)

// Elsewhere the desktop app is untested; most Linux setups run the itan
// command as a service or in Docker instead.

func openBrowser(url string) {
	exec.Command("xdg-open", url).Start()
}

func copyText(string) error { return errors.New("copying isn't supported here") }

func alert(title, msg string) { log.Printf("%s: %s", title, msg) }

func onReopen(func()) {}

func loginItemSupported() bool     { return false }
func loginItemUnsupported() string { return "" }
func loginItemEnabled() bool       { return false }
func setLoginItem(bool) error      { return errors.New("not supported") }
