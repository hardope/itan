package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Transcoding re-encodes video to H.264, which every browser plays. A
// hardware encoder is used when one works; otherwise libx264.

const (
	transcodeMaxHeight = 1080 // 4K is scaled down: smaller, faster, fine for Wi-Fi
	vaapiDevice        = "/dev/dri/renderD128"
)

type encoder struct {
	name string // ffmpeg encoder name, e.g. "h264_videotoolbox"
	kind string // software | videotoolbox | vaapi | nvenc
}

var encoders = map[string]encoder{
	"software":     {"libx264", "software"},
	"videotoolbox": {"h264_videotoolbox", "videotoolbox"},
	"vaapi":        {"h264_vaapi", "vaapi"},
	"nvenc":        {"h264_nvenc", "nvenc"},
}

// encoderPicker detects a working encoder on first use and remembers it.
type encoderPicker struct {
	ffmpeg string
	pref   string

	once sync.Once
	enc  encoder
}

func (p *encoderPicker) get() encoder {
	p.once.Do(func() {
		p.enc = pickEncoder(p.ffmpeg, p.pref)
		log.Printf("transcoding: using %s", p.enc.name)
	})
	return p.enc
}

func pickEncoder(ffmpeg, pref string) encoder {
	var candidates []string
	switch pref {
	case "none":
	case "auto":
		switch runtime.GOOS {
		case "darwin":
			candidates = []string{"videotoolbox"}
		case "linux":
			candidates = []string{"nvenc"}
			if _, err := os.Stat(vaapiDevice); err == nil {
				candidates = append(candidates, "vaapi")
			}
		case "windows":
			candidates = []string{"nvenc"}
		}
	default:
		candidates = []string{pref}
	}
	for _, c := range candidates {
		if err := testEncoder(ffmpeg, encoders[c]); err != nil {
			log.Printf("transcoding: %s unavailable: %v", encoders[c].name, err)
			continue
		}
		return encoders[c]
	}
	return encoders["software"]
}

// testEncoder encodes a fraction of a second of a test pattern through the
// same arguments real sessions use. Being listed by `ffmpeg -encoders`
// isn't enough: the GPU or driver may be missing.
func testEncoder(ffmpeg string, e encoder) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, e.inputArgs()...)
	args = append(args, "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30", "-t", "0.5")
	args = append(args, e.videoArgs(1280, 720)...)
	args = append(args, "-f", "null", "-")
	out, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, firstLine(string(out)))
	}
	return nil
}

// inputArgs go before -i.
func (e encoder) inputArgs() []string {
	if e.kind == "vaapi" {
		return []string{"-vaapi_device", vaapiDevice}
	}
	return nil
}

// videoArgs returns the filter and encoder arguments for a w x h source
// (0 x 0 if unknown). Output is 8-bit 4:2:0 H.264, at most 1080p, with a
// keyframe at every segment boundary so HLS segments cut cleanly.
func (e encoder) videoArgs(w, h int) []string {
	outW, outH := fitHeight(w, h, transcodeMaxHeight)
	bitrate := bitrateFor(outH)

	scale := ""
	if outW > 0 && (outW != w || outH != h) {
		scale = fmt.Sprintf("scale=%d:%d,", outW, outH)
	}

	var filter string
	var codecArgs []string
	switch e.kind {
	case "vaapi":
		// Frames are uploaded to the GPU, then scaled there.
		filter = "format=nv12,hwupload"
		if scale != "" {
			filter += fmt.Sprintf(",scale_vaapi=w=%d:h=%d", outW, outH)
		}
		codecArgs = []string{"-b:v", bitrate.target, "-maxrate", bitrate.max}
	case "videotoolbox":
		filter = scale + "format=nv12"
		codecArgs = []string{"-b:v", bitrate.target, "-profile:v", "high"}
	case "nvenc":
		filter = scale + "format=yuv420p"
		codecArgs = []string{"-preset", "p4", "-b:v", bitrate.target, "-maxrate", bitrate.max, "-bufsize", bitrate.buf, "-profile:v", "high"}
	default:
		filter = scale + "format=yuv420p"
		codecArgs = []string{"-preset", "veryfast", "-crf", "23", "-maxrate", bitrate.max, "-bufsize", bitrate.buf, "-profile:v", "high"}
	}

	args := []string{"-vf", filter, "-c:v", e.name}
	args = append(args, codecArgs...)
	// Keyframes exactly at segment boundaries, and a long GOP in between:
	// ffmpeg's default of 12 frames (which hardware encoders inherit) would
	// spend most of the bitrate on keyframes.
	return append(args, "-g", "300", "-force_key_frames", "expr:gte(t,n_forced*"+hlsSegmentSecs+")")
}

// fitHeight scales w x h down to at most maxH tall, keeping even dimensions.
func fitHeight(w, h, maxH int) (int, int) {
	if w <= 0 || h <= 0 || h <= maxH {
		return w, h
	}
	nw := int(float64(w)*float64(maxH)/float64(h)/2+0.5) * 2
	return nw, maxH
}

type bitrateSet struct{ target, max, buf string }

func bitrateFor(height int) bitrateSet {
	switch {
	case height > 0 && height <= 480:
		return bitrateSet{"1500k", "2200k", "3000k"}
	case height > 0 && height <= 720:
		return bitrateSet{"3000k", "4500k", "6000k"}
	default:
		return bitrateSet{"6000k", "9000k", "12000k"}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
