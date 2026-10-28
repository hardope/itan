package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hardope/itan/internal/config"
)

// HLS: for files the browser can't play directly, ffmpeg repackages them
// into HLS (fragmented MP4 segments). Usually the video stream is copied
// (remux), which costs little CPU. If the browser can't decode the video
// codec (HEVC on most browsers, MPEG-4/XviD, VC-1, ...), it is transcoded
// to H.264 instead (see encoder.go). Audio the browser can't play (DTS,
// AC3, ...) is converted to AAC, which is cheap.
//
// One session per player. A session is one ffmpeg process writing segments
// from a start position into its own temp folder. Seeking past what has been
// produced restarts the session at the new position (the UI keeps track of
// the offset). Sessions with no requests for hlsIdleTimeout are stopped and
// their files deleted.

const (
	hlsMaxSessions = 4
	hlsIdleTimeout = 5 * time.Minute
	// A transcode slot can be taken over after this long without requests.
	hlsTranscodeIdle = 30 * time.Second
	hlsReadyWait     = 30 * time.Second
	hlsSegmentSecs   = "6"
)

var (
	sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
	hlsFileRe   = regexp.MustCompile(`^(index\.m3u8|init\.mp4|seg\d{5}\.m4s)$`)

	// Video codecs that can be copied into HLS fMP4 for browsers that decode them.
	copyableCodecs = map[string]bool{"h264": true, "hevc": true, "vp9": true, "av1": true}

	errTranscodeDisabled = errors.New("transcoding is disabled on this server")
	errTranscodeBusy     = errors.New("the server is already transcoding as many videos as it's allowed to")
)

type hlsManager struct {
	dir           string
	ffmpeg        string
	encoders      *encoderPicker
	maxTranscodes int

	mu       sync.Mutex
	sessions map[string]*hlsSession
}

type hlsSession struct {
	id         string
	itemID     int64
	start      float64
	encoder    string // "" when the video is copied
	dir        string
	cancel     context.CancelFunc
	done       chan struct{} // closed when ffmpeg exits
	lastAccess atomic.Int64  // unix seconds
	stderr     *limitedBuffer
	err        error // ffmpeg exit error; read only after done is closed
}

func newHLSManager(cfg *config.Config) *hlsManager {
	m := &hlsManager{
		dir:           filepath.Join(cfg.DataDir, "transcode"),
		ffmpeg:        cfg.FFmpeg,
		encoders:      &encoderPicker{ffmpeg: cfg.FFmpeg, pref: cfg.HWAccel},
		maxTranscodes: cfg.MaxTranscodes,
		sessions:      map[string]*hlsSession{},
	}
	os.RemoveAll(m.dir) // leftovers from a previous run
	go m.janitor()
	return m
}

type hlsSource struct {
	path          string
	videoCodec    string
	audioCodec    string
	width, height int
	transcode     bool
}

// start (re)starts the session id at start seconds into the file and waits
// until the first segment is ready.
func (m *hlsManager) start(ctx context.Context, id string, itemID int64, start float64, src hlsSource) (*hlsSession, error) {
	var enc *encoder
	if src.transcode {
		if m.maxTranscodes <= 0 {
			return nil, errTranscodeDisabled
		}
		e := m.encoders.get()
		enc = &e
	}

	m.mu.Lock()
	if old := m.sessions[id]; old != nil {
		delete(m.sessions, id)
		m.mu.Unlock()
		old.stop()
		m.mu.Lock()
	}
	if enc != nil && m.transcodingLocked() >= m.maxTranscodes && !m.evictIdleTranscodeLocked() {
		m.mu.Unlock()
		return nil, errTranscodeBusy
	}
	for len(m.sessions) >= hlsMaxSessions {
		m.evictOldestLocked()
	}
	s := &hlsSession{
		id:     id,
		itemID: itemID,
		start:  start,
		dir:    filepath.Join(m.dir, id),
		done:   make(chan struct{}),
		stderr: &limitedBuffer{max: 4096},
	}
	if enc != nil {
		s.encoder = enc.name
	}
	s.lastAccess.Store(time.Now().Unix())
	m.sessions[id] = s
	m.mu.Unlock()

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		m.remove(s)
		return nil, err
	}
	procCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	cmd := exec.CommandContext(procCtx, m.ffmpeg, ffmpegHLSArgs(src, enc, start, s.dir)...)
	cmd.Stderr = s.stderr
	if err := cmd.Start(); err != nil {
		cancel()
		close(s.done)
		m.remove(s)
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	go func() {
		s.err = cmd.Wait()
		close(s.done)
		if s.err != nil && procCtx.Err() == nil {
			log.Printf("hls %s: ffmpeg failed: %v: %s", id, s.err, s.stderr)
		}
	}()

	if err := s.waitReady(ctx); err != nil {
		m.remove(s)
		return nil, err
	}
	return s, nil
}

// ffmpegHLSArgs builds the ffmpeg command line. enc is nil to copy the video.
func ffmpegHLSArgs(src hlsSource, enc *encoder, start float64, dir string) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if start > 0 {
		// Before -i: fast seek. Copying starts at the keyframe at or before
		// start; transcoding decodes from there and starts exactly at start.
		args = append(args, "-ss", fmt.Sprintf("%.3f", start))
	}
	if enc != nil {
		args = append(args, enc.inputArgs()...)
		// Encode at most 2x real time after a 30s head start, so a paused or
		// abandoned video doesn't keep a CPU core busy until the end of the file.
		args = append(args, "-readrate", "2", "-readrate_initial_burst", "30")
	}
	args = append(args,
		"-i", src.path,
		"-map", "0:v:0", "-map", "0:a:0?",
		"-sn", "-dn",
	)
	switch {
	case enc != nil:
		args = append(args, enc.videoArgs(src.width, src.height)...)
	case src.videoCodec == "hevc":
		// Apple (and Chrome with hardware decode) need the hvc1 tag for HEVC in MP4.
		args = append(args, "-c:v", "copy", "-tag:v", "hvc1")
	default:
		args = append(args, "-c:v", "copy")
	}
	if src.audioCodec == "aac" {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "192k")
	}
	return append(args,
		"-max_muxing_queue_size", "2048",
		"-f", "hls",
		"-hls_time", hlsSegmentSecs,
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_segment_type", "fmp4",
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_flags", "independent_segments+temp_file",
		"-hls_segment_filename", filepath.Join(dir, "seg%05d.m4s"),
		filepath.Join(dir, "index.m3u8"),
	)
}

// waitReady blocks until the playlist lists at least one segment, ffmpeg
// fails, or the wait times out.
func (s *hlsSession) waitReady(ctx context.Context) error {
	deadline := time.After(hlsReadyWait)
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		if b, err := os.ReadFile(filepath.Join(s.dir, "index.m3u8")); err == nil && bytes.Contains(b, []byte("#EXTINF")) {
			return nil
		}
		select {
		case <-s.done:
			// A very short file may finish before the first poll.
			if b, err := os.ReadFile(filepath.Join(s.dir, "index.m3u8")); err == nil && bytes.Contains(b, []byte("#EXTINF")) {
				return nil
			}
			return fmt.Errorf("ffmpeg exited: %v: %s", s.err, s.stderr)
		case <-deadline:
			return errors.New("timed out waiting for ffmpeg")
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (s *hlsSession) stop() {
	if s.cancel != nil {
		s.cancel()
	}
	<-s.done
	os.RemoveAll(s.dir)
}

func (m *hlsManager) get(id string) *hlsSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s != nil {
		s.lastAccess.Store(time.Now().Unix())
	}
	return s
}

func (m *hlsManager) remove(s *hlsSession) {
	m.mu.Lock()
	if m.sessions[s.id] == s {
		delete(m.sessions, s.id)
	}
	m.mu.Unlock()
	s.stop()
}

func (m *hlsManager) transcodingLocked() int {
	n := 0
	for _, s := range m.sessions {
		if s.encoder != "" {
			n++
		}
	}
	return n
}

// evictIdleTranscodeLocked frees a transcode slot held by a session nobody
// has fetched from for hlsTranscodeIdle (a closed tab, or a long pause: that
// player restarts the stream when it resumes). Reports whether one was freed.
func (m *hlsManager) evictIdleTranscodeLocked() bool {
	cutoff := time.Now().Add(-hlsTranscodeIdle).Unix()
	var victim *hlsSession
	for _, s := range m.sessions {
		if s.encoder != "" && s.lastAccess.Load() < cutoff &&
			(victim == nil || s.lastAccess.Load() < victim.lastAccess.Load()) {
			victim = s
		}
	}
	if victim == nil {
		return false
	}
	delete(m.sessions, victim.id)
	go victim.stop()
	return true
}

// evictOldestLocked stops the least recently used session. Caller holds m.mu;
// the process is stopped in the background so the lock isn't held while waiting.
func (m *hlsManager) evictOldestLocked() {
	var oldest *hlsSession
	for _, s := range m.sessions {
		if oldest == nil || s.lastAccess.Load() < oldest.lastAccess.Load() {
			oldest = s
		}
	}
	if oldest != nil {
		delete(m.sessions, oldest.id)
		go oldest.stop()
	}
}

func (m *hlsManager) janitor() {
	for range time.Tick(30 * time.Second) {
		cutoff := time.Now().Add(-hlsIdleTimeout).Unix()
		m.mu.Lock()
		var idle []*hlsSession
		for id, s := range m.sessions {
			if s.lastAccess.Load() < cutoff {
				delete(m.sessions, id)
				idle = append(idle, s)
			}
		}
		m.mu.Unlock()
		for _, s := range idle {
			s.stop()
		}
	}
}

// ---- handlers ----

// startHLS starts (or restarts at a new position) an HLS session.
// Body: {"session": "<client-chosen id>", "start": 123.4, "transcode": false}
//
// The client sets transcode when its browser can't decode the video codec;
// the server also transcodes any codec that can't be copied into HLS at all.
func (s *Server) startHLS(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		Session   string  `json:"session"`
		Start     float64 `json:"start"`
		Transcode bool    `json:"transcode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil ||
		!sessionIDRe.MatchString(body.Session) || body.Start < 0 {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	path, err := s.filePath(r.Context(), id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	var videoCodec, audioCodec sql.NullString
	var durationMS, width, height sql.NullInt64
	if err := s.db.QueryRowContext(r.Context(), `
		SELECT video_codec, duration_ms, width, height,
		       (SELECT codec FROM streams WHERE item_id = items.id AND type = 'audio'
		         ORDER BY stream_index LIMIT 1)
		  FROM items WHERE id = ?`, id).Scan(&videoCodec, &durationMS, &width, &height, &audioCodec); err != nil {
		serverError(w, r, err)
		return
	}
	src := hlsSource{
		path:       path,
		videoCodec: videoCodec.String,
		audioCodec: audioCodec.String,
		width:      int(width.Int64),
		height:     int(height.Int64),
		// Unknown codec (probe failed): transcoding is the safe choice.
		transcode: body.Transcode || !copyableCodecs[videoCodec.String],
	}
	// Starting at or past the end would produce nothing.
	if durationMS.Valid && body.Start*1000 > float64(durationMS.Int64)-1000 {
		body.Start = 0
	}

	sess, err := s.hls.start(r.Context(), body.Session, id, body.Start, src)
	if errors.Is(err, errTranscodeDisabled) || errors.Is(err, errTranscodeBusy) {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		log.Printf("hls item %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "could not prepare this video for streaming")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url":     "/hls/" + sess.id + "/index.m3u8",
		"start":   sess.start,
		"encoder": sess.encoder, // "" when the video is copied
	})
}

func (s *Server) stopHLS(w http.ResponseWriter, r *http.Request) {
	if sess := s.hls.get(r.PathValue("session")); sess != nil {
		s.hls.remove(sess)
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveHLS serves a session's playlist and segments.
func (s *Server) serveHLS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	sess := s.hls.get(r.PathValue("session"))
	if sess == nil || !hlsFileRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(sess.dir, name)

	if name == "index.m3u8" {
		b, err := os.ReadFile(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// While ffmpeg is still writing, the playlist looks "live" and players
		// would jump to the newest segment. EXT-X-START pins playback to the start.
		b = bytes.Replace(b, []byte("#EXTM3U\n"), []byte("#EXTM3U\n#EXT-X-START:TIME-OFFSET=0\n"), 1)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, path)
}

// limitedBuffer keeps the first max bytes written (ffmpeg stderr).
type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.buf.String())
}
