// Command itan-desktop is Itan as a desktop app. The server runs in the
// background, with an icon in the menu bar (macOS) or the notification
// area (Windows) to open Itan, see the addresses other devices can use,
// start it at login and quit. It uses the ffmpeg and ffprobe next to it,
// as installed with the app.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"

	"github.com/hardope/itan/internal/app"
	"github.com/hardope/itan/internal/config"
)

// version is set by release builds: -ldflags "-X main.version=0.2.0".
var version = "dev"

func main() {
	cfg := config.Defaults()
	if os.Getenv("ITAN_DATA") == "" {
		cfg.DataDir = config.AppDataDir()
	}
	background := flag.Bool("background", false, "start without opening the browser (used at login)")
	flag.StringVar(&cfg.DataDir, "data", cfg.DataDir, "data directory: database, thumbnails and log")
	flag.StringVar(&cfg.Addr, "addr", cfg.Addr, "listen address; the next free port is used if it's taken")
	flag.Parse()
	cfg.Version = version
	cfg.FFmpeg = bundled("ffmpeg", cfg.FFmpeg)
	cfg.FFprobe = bundled("ffprobe", cfg.FFprobe)
	if err := cfg.Check(); err != nil {
		fail(err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		fail(err)
	}
	if f := openLog(cfg.DataDir); f != nil {
		defer f.Close()
	}
	_, err := os.Stat(filepath.Join(cfg.DataDir, "itan.db"))
	firstRun := errors.Is(err, os.ErrNotExist)

	ln, running, err := listen(cfg.Addr)
	if running != "" {
		// Opening the app again while it runs just opens Itan.
		log.Printf("itan: already running at %s", running)
		if !*background {
			openBrowser(running)
		}
		return
	}
	if err != nil {
		fail(fmt.Errorf("no free port to listen on: %w", err))
	}
	a, err := app.Start(cfg, ln)
	if err != nil {
		fail(err)
	}
	if loginItemSupported() {
		// Keep the login item pointing at this copy of the app, and turn
		// it on the first time on macOS (the Windows installer asks).
		if firstRun && runtime.GOOS == "darwin" || loginItemEnabled() {
			if err := setLoginItem(true); err != nil {
				log.Printf("itan: open at login: %v", err)
			}
		}
	}
	if !*background {
		openBrowser(a.LocalURL())
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-sig:
		case err := <-a.Done():
			if err != nil {
				log.Printf("itan: server stopped: %v", err)
				alert("Itan stopped", err.Error())
			}
		}
		systray.Quit()
	}()
	// The tray calls the exit function when the system quits the app (at
	// logout, say); after Quit it just returns, at least on macOS.
	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			if err := a.Close(); err != nil {
				log.Printf("itan: %v", err)
			}
			log.Printf("itan: quit")
		})
	}
	systray.Run(func() { trayReady(a) }, shutdown)
	shutdown()
}

// listen opens the port in addr, or the next free one. If Itan itself is
// already on a port, it returns that copy's address instead.
func listen(addr string) (net.Listener, string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", err
	}
	p, err := strconv.Atoi(port)
	if err != nil || p == 0 {
		ln, err := net.Listen("tcp", addr)
		return ln, "", err
	}
	var first error
	for try := p; try < p+20; try++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(try)))
		if err == nil {
			return ln, "", nil
		}
		if first == nil {
			first = err
		}
		if u := "http://localhost:" + strconv.Itoa(try); isItan(u) {
			return nil, u, nil
		}
	}
	return nil, "", first
}

// isItan reports whether an Itan server answers at u.
func isItan(u string) bool {
	c := http.Client{Timeout: 2 * time.Second}
	res, err := c.Get(u + "/api/info")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var info struct {
		Source string `json:"source"`
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&info) == nil && info.Source != ""
}

// bundled returns the copy of tool installed next to this program, unless
// the setting was changed from the default (the tool's name alone).
func bundled(tool, setting string) string {
	if setting != tool {
		return setting
	}
	exe, err := os.Executable()
	if err != nil {
		return setting
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	name := tool
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return setting
}

// openLog sends the log to itan.log in the data folder, keeping the
// previous one as itan.log.1 once it grows past 5 MB.
func openLog(dir string) *os.File {
	path := filepath.Join(dir, "itan.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("itan: log file: %v", err)
		return nil
	}
	// The file first: a Windows app has no console, so writing to stderr
	// fails, and MultiWriter stops at the first failure.
	log.SetOutput(io.MultiWriter(f, os.Stderr))
	return f
}

// fail tells the user why Itan can't start, and exits.
func fail(err error) {
	log.Printf("itan: %v", err)
	alert("Itan couldn't start", err.Error())
	os.Exit(1)
}
