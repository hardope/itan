package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"

	"github.com/hardope/itan/internal/proc"
)

func openBrowser(url string) {
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func copyText(s string) error {
	cmd := proc.Command(context.Background(), "clip")
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

var messageBox = syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")

func alert(title, msg string) {
	const iconError = 0x10 // MB_ICONERROR
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(msg)
	messageBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), iconError)
}

// onReopen does nothing on Windows: opening the app again starts a second
// copy, which finds this one and opens the browser.
func onReopen(func()) {}

// The login item is a value under the Run key, as the installer sets it.
const (
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = "Itan"
)

func loginItemSupported() bool     { return true }
func loginItemUnsupported() string { return "" }

func loginItemEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

func setLoginItem(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runValue, `"`+exe+`" -background`)
}
