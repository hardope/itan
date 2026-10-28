// Package config reads server settings from flags and environment variables.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
}

// Load parses flags, falling back to ITAN_* environment variables.
func Load() (*Config, error) {
	c := &Config{}
	var libs string

	flag.StringVar(&c.Addr, "addr", env("ITAN_ADDR", ":8080"), "listen address")
	flag.StringVar(&c.DataDir, "data", env("ITAN_DATA", "./data"), "data directory")
	flag.DurationVar(&c.ScanInterval, "scan-interval", envDuration("ITAN_SCAN_INTERVAL", 30*time.Minute), "periodic rescan interval (0 = off)")
	flag.StringVar(&c.FFprobe, "ffprobe", env("ITAN_FFPROBE", "ffprobe"), "ffprobe binary")
	flag.StringVar(&c.FFmpeg, "ffmpeg", env("ITAN_FFMPEG", "ffmpeg"), "ffmpeg binary")
	flag.StringVar(&c.HWAccel, "hwaccel", env("ITAN_HWACCEL", "auto"), "transcoding encoder: auto, none, videotoolbox, vaapi, nvenc")
	flag.IntVar(&c.MaxTranscodes, "max-transcodes", envInt("ITAN_MAX_TRANSCODES", 2), "simultaneous transcodes (0 = disable transcoding)")
	flag.StringVar(&libs, "libraries", env("ITAN_LIBRARIES", ""), `libraries to register, "Name=/path;Other=/path2"`)
	flag.BoolVar(&c.ResetPIN, "reset-pin", false, "remove the edit PIN (if it's forgotten) and exit")
	flag.Parse()

	switch c.HWAccel {
	case "auto", "none", "videotoolbox", "vaapi", "nvenc":
	default:
		return nil, fmt.Errorf("unknown -hwaccel %q (want auto, none, videotoolbox, vaapi or nvenc)", c.HWAccel)
	}

	var err error
	if c.DataDir, err = filepath.Abs(c.DataDir); err != nil {
		return nil, err
	}
	if c.Libraries, err = parseLibraries(libs); err != nil {
		return nil, err
	}
	return c, nil
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
