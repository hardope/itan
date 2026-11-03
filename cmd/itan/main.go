package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/hardope/itan/internal/app"
	"github.com/hardope/itan/internal/config"
	"github.com/hardope/itan/internal/db"
	"github.com/hardope/itan/internal/server"
)

// version is set by release builds: -ldflags "-X main.version=0.1.0".
var version = "dev"

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
	cfg.Version = buildVersion()
	if cfg.ShowVersion {
		fmt.Println("itan", cfg.Version)
		return nil
	}

	if cfg.ResetPIN {
		database, err := db.Open(context.Background(), cfg.DataDir)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer database.Close()
		if err := server.ResetPIN(context.Background(), database); err != nil {
			return err
		}
		log.Printf("itan: the edit PIN is removed; set a new one in Settings")
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := app.Start(cfg, nil)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case err := <-a.Done():
		a.Close()
		return err
	}
	return a.Close()
}

// buildVersion is the release version, or for other builds the one Go
// records from the module (go install ...@v0.1.0) or the git checkout.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return version
}
