package server

import (
	"slices"
	"strings"
	"testing"
)

func TestFitHeight(t *testing.T) {
	tests := []struct{ w, h, wantW, wantH int }{
		{3840, 2160, 1920, 1080},
		{3840, 1600, 2592, 1080}, // scope film keeps its aspect ratio
		{1920, 1080, 1920, 1080},
		{1280, 720, 1280, 720},
		{0, 0, 0, 0},
	}
	for _, tt := range tests {
		if w, h := fitHeight(tt.w, tt.h, 1080); w != tt.wantW || h != tt.wantH {
			t.Errorf("fitHeight(%d, %d) = %dx%d, want %dx%d", tt.w, tt.h, w, h, tt.wantW, tt.wantH)
		}
	}
}

func TestVideoArgs(t *testing.T) {
	arg := func(args []string, flag string) string {
		if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}

	sw := encoders["software"].videoArgs(3840, 2160)
	if got := arg(sw, "-vf"); got != "scale=1920:1080,format=yuv420p" {
		t.Errorf("software 4K filter = %q", got)
	}
	if arg(sw, "-c:v") != "libx264" || arg(sw, "-maxrate") != "9000k" {
		t.Errorf("software args = %v", sw)
	}
	if !strings.Contains(arg(sw, "-force_key_frames"), "n_forced*"+hlsSegmentSecs) {
		t.Errorf("keyframes not aligned to segments: %v", sw)
	}

	// 720p source: no scaling, lower bitrate.
	if got := arg(encoders["videotoolbox"].videoArgs(1280, 720), "-vf"); got != "format=nv12" {
		t.Errorf("videotoolbox 720p filter = %q", got)
	}
	if got := arg(encoders["videotoolbox"].videoArgs(1280, 720), "-b:v"); got != "3000k" {
		t.Errorf("videotoolbox 720p bitrate = %q", got)
	}

	// VAAPI scales on the GPU after upload.
	if got := arg(encoders["vaapi"].videoArgs(3840, 2160), "-vf"); got != "format=nv12,hwupload,scale_vaapi=w=1920:h=1080" {
		t.Errorf("vaapi 4K filter = %q", got)
	}
}
