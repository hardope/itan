package server

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The edit PIN keeps people on the network from changing the library.
// Watching, favorites and resume points stay open to everyone; editing
// titles, tags, ratings, series, folders and backups needs the PIN once per
// device. With no PIN set, everything is open, as before.
//
// Unlocking stores a random token in an HttpOnly cookie and its hash in
// edit_sessions. A PIN is only 4 to 8 digits, so the hash alone wouldn't
// stop a determined guesser: wrong attempts are limited per address.

const (
	pinSettingKey  = "edit_pin"
	sessionCookie  = "itan_edit"
	sessionMaxIdle = 30 * 24 * time.Hour
	freeAttempts   = 5
	maxLockout     = 15 * time.Minute
)

// pinIterations is the PBKDF2 work factor for new PIN hashes (stored with
// each hash, so it can change). Tests lower it.
var pinIterations = 600_000

type pinGuard struct {
	mu    sync.Mutex
	fails map[string]*pinFails // by client address
}

type pinFails struct {
	count int
	until time.Time
}

func newPinGuard() *pinGuard { return &pinGuard{fails: map[string]*pinFails{}} }

// wait says how long a client must wait before trying again.
func (g *pinGuard) wait(addr string, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if f := g.fails[addr]; f != nil && now.Before(f.until) {
		return f.until.Sub(now)
	}
	return 0
}

// failed records a wrong PIN. After freeAttempts, each further miss locks
// the address out for twice as long, from 30 seconds up to maxLockout.
func (g *pinGuard) failed(addr string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.fails[addr]
	if f == nil {
		f = &pinFails{}
		g.fails[addr] = f
	}
	f.count++
	if over := f.count - freeAttempts; over >= 0 {
		lock := time.Duration(float64(30*time.Second) * math.Pow(2, float64(over)))
		f.until = now.Add(min(lock, maxLockout))
	}
}

func (g *pinGuard) succeeded(addr string) {
	g.mu.Lock()
	delete(g.fails, addr)
	g.mu.Unlock()
}

func validPIN(pin string) bool {
	if len(pin) < 4 || len(pin) > 8 {
		return false
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// hashPIN returns "pbkdf2-sha256$<iterations>$<salt>$<hash>".
func hashPIN(pin string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pin, salt, pinIterations, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pinIterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

func checkPIN(stored, pin string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err != nil || err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pin, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// storedPIN returns the PIN hash, or "" if no PIN is set.
func (s *Server) storedPIN(ctx context.Context) (string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, pinSettingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var hash string
	err = json.Unmarshal([]byte(raw), &hash)
	return hash, err
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// unlocked reports whether the request may edit: no PIN is set, or it
// carries a live session cookie.
func (s *Server) unlocked(r *http.Request) (bool, error) {
	stored, err := s.storedPIN(r.Context())
	if err != nil || stored == "" {
		return err == nil, err
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false, nil
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(r.Context(), `
		UPDATE edit_sessions SET last_used_at = ? WHERE token_hash = ? AND last_used_at > ?`,
		now, tokenHash(c.Value), now-int64(sessionMaxIdle/time.Second))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// requireEdit wraps handlers that change the library.
func (s *Server) requireEdit(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, err := s.unlocked(r)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Enter the PIN to make changes.", "locked": true})
			return
		}
		h(w, r)
	}
}

// startSession unlocks this device: a new token in a cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO edit_sessions(token_hash, device) VALUES (?, ?)`,
		tokenHash(token), deviceName(r.UserAgent())); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		MaxAge: int(sessionMaxIdle / time.Second), HttpOnly: true, Secure: r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func (s *Server) endSession(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, err := s.db.ExecContext(r.Context(), `DELETE FROM edit_sessions WHERE token_hash = ?`, tokenHash(c.Value)); err != nil {
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return nil
}

// deviceName is a rough label for a user agent ("Safari on iPhone").
func deviceName(ua string) string {
	browser := "Browser"
	for _, b := range []string{"Edg", "Firefox", "Chrome", "Safari"} {
		if strings.Contains(ua, b+"/") {
			browser = map[string]string{"Edg": "Edge"}[b]
			if browser == "" {
				browser = b
			}
			break
		}
	}
	for _, os := range []string{"iPhone", "iPad", "Android", "Mac OS X", "Windows", "Linux"} {
		if strings.Contains(ua, os) {
			return browser + " on " + strings.TrimSuffix(os, " OS X")
		}
	}
	return browser
}

func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type authJSON struct {
	PinSet   bool `json:"pin_set"`
	Unlocked bool `json:"unlocked"`
}

func (s *Server) authState(w http.ResponseWriter, r *http.Request) {
	stored, err := s.storedPIN(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	ok, err := s.unlocked(r)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, authJSON{PinSet: stored != "", Unlocked: ok})
}

// verifyPIN checks a PIN for this client, applying the attempt limit. It
// writes the error response and returns false if the PIN isn't accepted.
func (s *Server) verifyPIN(w http.ResponseWriter, r *http.Request, stored, pin string) bool {
	addr, now := clientAddr(r), time.Now()
	if wait := s.pins.wait(addr, now); wait > 0 {
		secs := int(math.Ceil(wait.Seconds()))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": fmt.Sprintf("Too many wrong tries. Try again in %s.", plainDuration(secs)), "retry_after": secs,
		})
		return false
	}
	if !checkPIN(stored, pin) {
		s.pins.failed(addr, now)
		writeError(w, http.StatusForbidden, "That PIN isn't right.")
		return false
	}
	s.pins.succeeded(addr)
	return true
}

func plainDuration(secs int) string {
	if secs < 60 {
		return fmt.Sprintf("%d seconds", secs)
	}
	m := (secs + 59) / 60
	if m == 1 {
		return "a minute"
	}
	return fmt.Sprintf("%d minutes", m)
}

// unlock: {"pin": "1234"}
func (s *Server) unlock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	stored, err := s.storedPIN(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	if stored == "" {
		writeJSON(w, http.StatusOK, authJSON{Unlocked: true})
		return
	}
	if !s.verifyPIN(w, r, stored, body.PIN) {
		return
	}
	if err := s.startSession(w, r); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, authJSON{PinSet: true, Unlocked: true})
}

func (s *Server) lock(w http.ResponseWriter, r *http.Request) {
	if err := s.endSession(w, r); err != nil {
		serverError(w, r, err)
		return
	}
	stored, _ := s.storedPIN(r.Context())
	writeJSON(w, http.StatusOK, authJSON{PinSet: stored != "", Unlocked: stored == ""})
}

// setPIN sets or changes the PIN: {"current": "1234", "pin": "5678"}.
// "current" is needed when a PIN is already set. Other devices are locked;
// this one stays unlocked.
func (s *Server) setPIN(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		PIN     string `json:"pin"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !validPIN(body.PIN) {
		writeError(w, http.StatusBadRequest, "The PIN must be 4 to 8 digits.")
		return
	}
	stored, err := s.storedPIN(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	if stored != "" && !s.verifyPIN(w, r, stored, body.Current) {
		return
	}
	hash, err := hashPIN(body.PIN)
	if err != nil {
		serverError(w, r, err)
		return
	}
	raw, _ := json.Marshal(hash)
	ctx := r.Context()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO settings(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, pinSettingKey, string(raw)); err != nil {
		serverError(w, r, err)
		return
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM edit_sessions`); err != nil {
		serverError(w, r, err)
		return
	}
	if err := s.startSession(w, r); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, authJSON{PinSet: true, Unlocked: true})
}

// removePIN turns the PIN off: {"current": "1234"}.
func (s *Server) removePIN(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	stored, err := s.storedPIN(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	if stored != "" && !s.verifyPIN(w, r, stored, body.Current) {
		return
	}
	if err := ResetPIN(r.Context(), s.db); err != nil {
		serverError(w, r, err)
		return
	}
	s.endSession(w, r)
	writeJSON(w, http.StatusOK, authJSON{Unlocked: true})
}

// ResetPIN removes the PIN and locks every device, for "itan -reset-pin"
// when the PIN is forgotten.
func ResetPIN(ctx context.Context, d *sql.DB) error {
	if _, err := d.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, pinSettingKey); err != nil {
		return err
	}
	_, err := d.ExecContext(ctx, `DELETE FROM edit_sessions`)
	return err
}
