package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Probe is the technical metadata ffprobe reports for a file.
type Probe struct {
	Container    string
	DurationMS   int64
	Bitrate      int64
	Width        int
	Height       int
	VideoCodec   string
	VideoProfile string
	FrameRate    float64
	HDR          string
	Title        string // embedded title tag, often junk
	Streams      []Stream
}

type Stream struct {
	Index     int
	Type      string // video | audio | subtitle
	Codec     string
	Language  string
	Title     string
	Channels  int
	IsDefault bool
	IsForced  bool
}

type ffprobeOutput struct {
	Format struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		BitRate    string            `json:"bit_rate"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		Index         int               `json:"index"`
		CodecType     string            `json:"codec_type"`
		CodecName     string            `json:"codec_name"`
		Profile       string            `json:"profile"`
		Width         int               `json:"width"`
		Height        int               `json:"height"`
		AvgFrameRate  string            `json:"avg_frame_rate"`
		Channels      int               `json:"channels"`
		ColorTransfer string            `json:"color_transfer"`
		Tags          map[string]string `json:"tags"`
		Disposition   map[string]int    `json:"disposition"`
		SideDataList  []map[string]any  `json:"side_data_list"`
	} `json:"streams"`
}

// RunProbe runs ffprobe on path.
func RunProbe(ctx context.Context, ffprobe, path string) (*Probe, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffprobe,
		"-v", "error", "-print_format", "json", "-show_format", "-show_streams", path).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("ffprobe: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("ffprobe: %w", err)
	}

	var raw ffprobeOutput
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("ffprobe output: %w", err)
	}

	p := &Probe{Container: containerName(path, raw.Format.FormatName)}
	if d, err := strconv.ParseFloat(raw.Format.Duration, 64); err == nil {
		p.DurationMS = int64(d * 1000)
	}
	p.Bitrate, _ = strconv.ParseInt(raw.Format.BitRate, 10, 64)
	p.Title = strings.TrimSpace(tag(raw.Format.Tags, "title"))

	for _, s := range raw.Streams {
		if s.CodecType != "video" && s.CodecType != "audio" && s.CodecType != "subtitle" {
			continue
		}
		// Cover art is stored as a one-frame video stream; ignore it.
		if s.Disposition["attached_pic"] == 1 {
			continue
		}
		p.Streams = append(p.Streams, Stream{
			Index:     s.Index,
			Type:      s.CodecType,
			Codec:     s.CodecName,
			Language:  tag(s.Tags, "language"),
			Title:     tag(s.Tags, "title"),
			Channels:  s.Channels,
			IsDefault: s.Disposition["default"] == 1,
			IsForced:  s.Disposition["forced"] == 1,
		})
		if s.CodecType == "video" && p.VideoCodec == "" {
			p.VideoCodec = s.CodecName
			p.VideoProfile = s.Profile
			p.Width, p.Height = s.Width, s.Height
			p.FrameRate = parseRate(s.AvgFrameRate)
			p.HDR = hdrKind(s.ColorTransfer, s.SideDataList)
		}
	}
	return p, nil
}

func tag(m map[string]string, key string) string {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	n, _ := strconv.ParseFloat(num, 64)
	if !ok {
		return n
	}
	d, _ := strconv.ParseFloat(den, 64)
	if d == 0 {
		return 0
	}
	return n / d
}

func hdrKind(transfer string, sideData []map[string]any) string {
	for _, sd := range sideData {
		if t, _ := sd["side_data_type"].(string); strings.Contains(t, "DOVI") {
			return "dolby_vision"
		}
	}
	switch transfer {
	case "smpte2084":
		return "hdr10"
	case "arib-std-b67":
		return "hlg"
	}
	return ""
}

// containerName normalises ffprobe's format list ("mov,mp4,m4a,...") using
// the extension to pick the specific one.
func containerName(path, formatName string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v":
		return "mp4"
	case ".mov":
		return "mov"
	case ".webm":
		return "webm"
	case ".mkv":
		return "matroska"
	}
	name, _, _ := strings.Cut(formatName, ",")
	return name
}

// QualityLabel maps a resolution to a familiar label. Width is checked too
// so letterboxed films (1920x800) still count as 1080p.
func QualityLabel(w, h int) string {
	switch {
	case w == 0 && h == 0:
		return ""
	case h >= 2000 || w >= 3800:
		return "4k"
	case h >= 1400 || w >= 2500:
		return "1440p"
	case h >= 1000 || w >= 1900:
		return "1080p"
	case h >= 700 || w >= 1260:
		return "720p"
	case h >= 560:
		return "576p"
	case h >= 470:
		return "480p"
	default:
		return "sd"
	}
}

// PlaybackMode decides how a browser can play the file:
//
//	direct    - the browser plays the bytes as they are
//	remux     - the video codec is fine but the container (e.g. MKV) or the
//	            audio (e.g. DTS) isn't: repackage with ffmpeg, video copied
//	transcode - the video codec needs re-encoding (e.g. HEVC on most browsers)
//
// This is deliberately conservative: what every modern browser plays.
func PlaybackMode(p *Probe) string {
	videoOK := map[string]bool{"h264": true, "vp8": true, "vp9": true, "av1": true}
	audioOK := map[string]bool{"aac": true, "mp3": true, "opus": true, "vorbis": true, "flac": true}

	if p.VideoCodec != "" && !videoOK[p.VideoCodec] {
		return "transcode"
	}
	audio := ""
	for _, s := range p.Streams {
		// Only the first audio track matters for playback.
		if s.Type == "audio" {
			audio = s.Codec
			break
		}
	}
	switch {
	case audio != "" && !audioOK[audio]:
		return "remux" // converting just the audio is cheap
	case p.Container == "mp4" || p.Container == "mov" || p.Container == "webm":
		return "direct"
	default:
		return "remux"
	}
}
