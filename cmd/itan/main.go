package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hardope/itan/internal/config"
	"github.com/hardope/itan/internal/db"
	"github.com/hardope/itan/internal/scanner"
	"github.com/hardope/itan/internal/server"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	if err := seed(ctx, database, cfg); err != nil {
		return err
	}

	sc := scanner.New(database, cfg.FFprobe)
	go scanLoop(ctx, sc, cfg.ScanInterval)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.New(database, cfg, sc).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: video responses stream for as long as playback lasts.
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	log.Printf("itan: data in %s", cfg.DataDir)
	logAddresses(cfg.Addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// seed creates the default profile and any libraries given on the command line.
func seed(ctx context.Context, database *sql.DB, cfg *config.Config) error {
	if _, err := database.ExecContext(ctx,
		`INSERT OR IGNORE INTO profiles(id, uid, name, is_admin) VALUES (1, ?, 'Default', 1)`,
		newUID()); err != nil {
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

// logAddresses prints the URLs other devices on the network can use.
func logAddresses(addr string) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	log.Printf("itan: open http://localhost:%s", port)
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
			log.Printf("itan: on your network: http://%s:%s", ipn.IP, port)
		}
	}
}

func newUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40 // UUID v4
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
