// Package config reads server settings from flags and environment variables.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Library struct {
	Name string
	Path string
}

type Config struct {
	Addr         string        // listen address, e.g. ":8080"
	DataDir      string        // database, thumbnails
	ScanInterval time.Duration // 0 disables periodic scans
	FFprobe      string        // path or name of ffprobe binary
	FFmpeg       string        // path or name of ffmpeg binary
	// Hardware encoder for transcoding: auto, none, videotoolbox, vaapi, nvenc.
	// "auto" tries the hardware encoders for this platform, then software.
	HWAccel string
	// Maximum simultaneous transcodes (CPU heavy). 0 disables transcoding.
	MaxTranscodes int
	// Libraries to create on startup if missing (handy for Docker).
	// Libraries can also be added from the web UI.
	Libraries []Library
	// ResetPIN removes the edit PIN and exits, for when it's forgotten.
	ResetPIN bool
	// ShowVersion prints the version and exits.
	ShowVersion bool
	// Version of this build, set by main (not a flag).
	Version string
}

// Defaults returns the settings used when no flags are given, taking
// ITAN_* environment variables into account.
func Defaults() *Config {
	c := &Config{
		Addr:          env("ITAN_ADDR", ":8080"),
		DataDir:       env("ITAN_DATA", defaultDataDir()),
		ScanInterval:  envDuration("ITAN_SCAN_INTERVAL", 30*time.Minute),
		FFprobe:       env("ITAN_FFPROBE", "ffprobe"),
		FFmpeg:        env("ITAN_FFMPEG", "ffmpeg"),
		HWAccel:       env("ITAN_HWACCEL", "auto"),
		MaxTranscodes: envInt("ITAN_MAX_TRANSCODES", 2),
	}
	return c
}

// Load parses flags over the defaults.
func Load() (*Config, error) {
	c := Defaults()
	libs := env("ITAN_LIBRARIES", "")

	flag.StringVar(&c.Addr, "addr", c.Addr, "listen address")
	flag.StringVar(&c.DataDir, "data", c.DataDir, "data directory: database and thumbnails")
	flag.DurationVar(&c.ScanInterval, "scan-interval", c.ScanInterval, "periodic rescan interval (0 = off)")
	flag.StringVar(&c.FFprobe, "ffprobe", c.FFprobe, "ffprobe binary")
	flag.StringVar(&c.FFmpeg, "ffmpeg", c.FFmpeg, "ffmpeg binary")
	flag.StringVar(&c.HWAccel, "hwaccel", c.HWAccel, "transcoding encoder: auto, none, videotoolbox, vaapi, nvenc")
	flag.IntVar(&c.MaxTranscodes, "max-transcodes", c.MaxTranscodes, "simultaneous transcodes (0 = disable transcoding)")
	flag.StringVar(&libs, "libraries", libs, `libraries to register, "Name=/path;Other=/path2"`)
	flag.BoolVar(&c.ResetPIN, "reset-pin", false, "remove the edit PIN (if it's forgotten) and exit")
	flag.BoolVar(&c.ShowVersion, "version", false, "print the version and exit")
	flag.Parse()

	var err error
	if c.Libraries, err = parseLibraries(libs); err != nil {
		return nil, err
	}
	return c, c.Check()
}

// Check validates the settings and makes the data directory absolute.
func (c *Config) Check() error {
	switch c.HWAccel {
	case "auto", "none", "videotoolbox", "vaapi", "nvenc":
	default:
		return fmt.Errorf("unknown -hwaccel %q (want auto, none, videotoolbox, vaapi or nvenc)", c.HWAccel)
	}
	var err error
	c.DataDir, err = filepath.Abs(c.DataDir)
	return err
}

// AppDataDir is the usual place for an app's data on this system:
// ~/Library/Application Support/Itan on macOS, %LOCALAPPDATA%\Itan on
// Windows, ~/.local/share/itan elsewhere.
func AppDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "data"
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Itan")
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "Itan")
		}
		return filepath.Join(home, "AppData", "Local", "Itan")
	default:
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			return filepath.Join(d, "itan")
		}
		return filepath.Join(home, ".local", "share", "itan")
	}
}

// defaultDataDir is AppDataDir, except that a ./data folder with a library
// in it (the default before 0.2) keeps being used.
func defaultDataDir() string {
	if _, err := os.Stat(filepath.Join("data", "itan.db")); err == nil {
		return "data"
	}
	return AppDataDir()
}

func parseLibraries(s string) ([]Library, error) {
	var out []Library
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, path, ok := strings.Cut(part, "=")
		if !ok {
			// Bare path: name it after the folder.
			path = name
			name = filepath.Base(path)
		}
		abs, err := filepath.Abs(strings.TrimSpace(path))
		if err != nil {
			return nil, fmt.Errorf("library %q: %w", part, err)
		}
		out = append(out, Library{Name: strings.TrimSpace(name), Path: abs})
	}
	return out, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
