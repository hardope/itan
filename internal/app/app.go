// Package app runs an Itan server: the database, background scans and the
// web server. The itan command and the desktop app both use it.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/hardope/itan/internal/config"
	"github.com/hardope/itan/internal/db"
	"github.com/hardope/itan/internal/scanner"
	"github.com/hardope/itan/internal/server"
)

// App is a running server.
type App struct {
	cfg      *config.Config
	db       *sql.DB
	web      *server.Server
	srv      *http.Server
	stopScan context.CancelFunc
	done     chan error
}

// Start opens the database, starts scanning and serves on ln, or on
// cfg.Addr when ln is nil. cfg.Addr is updated to the port in use.
func Start(cfg *config.Config, ln net.Listener) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	ctx, stopScan := context.WithCancel(context.Background())
	database, err := db.Open(ctx, cfg.DataDir)
	if err != nil {
		stopScan()
		return nil, fmt.Errorf("open database: %w", err)
	}
	fail := func(err error) (*App, error) {
		stopScan()
		database.Close()
		return nil, err
	}
	if err := seed(ctx, database, cfg); err != nil {
		return fail(err)
	}
	if ln == nil {
		if ln, err = net.Listen("tcp", cfg.Addr); err != nil {
			return fail(err)
		}
	}
	host, _, _ := net.SplitHostPort(cfg.Addr)
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	cfg.Addr = net.JoinHostPort(host, port)

	sc := scanner.New(database, cfg.FFprobe)
	web := server.New(database, cfg, sc)
	a := &App{cfg: cfg, db: database, web: web, stopScan: stopScan, done: make(chan error, 1),
		srv: &http.Server{
			Handler:           web.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			// No WriteTimeout: video responses stream for as long as playback lasts.
		}}
	go func() {
		if err := a.srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			a.done <- err
		}
		close(a.done)
	}()

	log.Printf("itan %s: data in %s", cfg.Version, cfg.DataDir)
	log.Printf("itan: open %s", a.LocalURL())
	for _, u := range a.LANURLs() {
		log.Printf("itan: on your network: %s", u)
	}
	go scanLoop(ctx, sc, cfg.ScanInterval)
	return a, nil
}

// LocalURL is the address for a browser on this computer.
func (a *App) LocalURL() string {
	_, port, _ := net.SplitHostPort(a.cfg.Addr)
	return "http://localhost:" + port
}

// LANURLs are the addresses other devices on the network can use.
func (a *App) LANURLs() []string { return server.LANURLs(a.cfg.Addr) }

// Done is closed when the web server stops, after sending the error if it
// stopped on its own.
func (a *App) Done() <-chan error { return a.done }

// Close stops serving (letting requests finish for a few seconds), stops
// scanning, writes pending folder files and closes the database.
func (a *App) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.srv.Shutdown(ctx)
	a.stopScan()
	a.web.Close()
	return a.db.Close()
}

// seed creates the default profile and any libraries given on the command line.
func seed(ctx context.Context, database *sql.DB, cfg *config.Config) error {
	if _, err := database.ExecContext(ctx,
		`INSERT OR IGNORE INTO profiles(id, uid, name, is_admin) VALUES (1, ?, 'Default', 1)`,
		db.NewUID()); err != nil {
		return err
	}
	for _, lib := range cfg.Libraries {
		if st, err := os.Stat(lib.Path); err != nil || !st.IsDir() {
			log.Printf("library %q: %s is not a folder, skipping", lib.Name, lib.Path)
			continue
		}
		if _, err := database.ExecContext(ctx,
			`INSERT INTO libraries(name, root_path) VALUES (?, ?) ON CONFLICT(root_path) DO NOTHING`,
			lib.Name, lib.Path); err != nil {
			return err
		}
	}
	return nil
}

func scanLoop(ctx context.Context, sc *scanner.Scanner, interval time.Duration) {
	scan := func() {
		if err := sc.Scan(ctx); err != nil && !errors.Is(err, scanner.ErrRunning) && ctx.Err() == nil {
			log.Printf("scan: %v", err)
		}
	}
	scan()
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			scan()
		}
	}
}
