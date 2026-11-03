package main

/*
#cgo LDFLAGS: -framework Cocoa
void itanWatchReopen(void);
*/
import "C"

import (
	"bytes"
	"encoding/xml"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
)

const bundleID = "com.hardope.itan"

func openBrowser(url string) {
	exec.Command("open", url).Start()
}

func copyText(s string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

func alert(title, msg string) {
	exec.Command("osascript",
		"-e", "on run argv",
		"-e", "display alert (item 1 of argv) message (item 2 of argv) as critical",
		"-e", "end run", title, msg).Run()
}

var reopened atomic.Pointer[func()]

// onReopen calls f when the app is opened again while it runs (from the
// Dock, Finder or Spotlight): macOS sends the running copy a "reopen"
// event instead of starting another.
func onReopen(f func()) {
	reopened.Store(&f)
	C.itanWatchReopen()
}

//export itanReopened
func itanReopened() {
	if f := reopened.Load(); f != nil {
		go (*f)()
	}
}

// appExecutable is this program, and whether it runs from an app bundle.
func appExecutable() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, strings.Contains(exe, ".app/Contents/MacOS/")
}

// translocated reports whether macOS runs the app from a temporary copy,
// as it does for a downloaded app opened before it's moved anywhere.
func translocated(exe string) bool { return strings.Contains(exe, "/AppTranslocation/") }

func loginItemSupported() bool {
	exe, ok := appExecutable()
	return ok && !translocated(exe)
}

func loginItemUnsupported() string {
	if exe, ok := appExecutable(); ok && translocated(exe) {
		return "move Itan to Applications first"
	}
	return ""
}

func launchAgent() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", bundleID+".plist")
}

func loginItemEnabled() bool {
	_, err := os.Stat(launchAgent())
	return err == nil
}

// setLoginItem adds or removes a launch agent that starts this copy of
// the app at login, in the background.
func setLoginItem(on bool) error {
	path := launchAgent()
	if !on {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, _ := appExecutable()
	var esc bytes.Buffer
	xml.EscapeText(&esc, []byte(exe))
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + bundleID + `</string>
	<key>AssociatedBundleIdentifiers</key>
	<array>
		<string>` + bundleID + `</string>
	</array>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc.String() + `</string>
		<string>-background</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`
	if old, err := os.ReadFile(path); err == nil && string(old) == plist {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(plist), 0o644)
}
