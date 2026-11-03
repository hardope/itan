package app

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hardope/itan/internal/config"
)

// Start serves on the port it was given (here any free one), reports it,
// and Close shuts everything down.
func TestStartAndClose(t *testing.T) {
	cfg := config.Defaults()
	cfg.Addr = "127.0.0.1:0"
	cfg.DataDir = filepath.Join(t.TempDir(), "data") // made by Start
	cfg.ScanInterval = 0
	if err := cfg.Check(); err != nil {
		t.Fatal(err)
	}
	a, err := Start(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(cfg.Addr, ":0") || !strings.HasPrefix(a.LocalURL(), "http://localhost:") {
		t.Fatalf("addr %s, local URL %s", cfg.Addr, a.LocalURL())
	}
	res, err := http.Get("http://" + cfg.Addr + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("/api/info: %d", res.StatusCode)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err, open := <-a.Done(); open || err != nil {
		t.Errorf("after Close, Done gave %v (open: %v)", err, open)
	}
	if _, err := http.Get("http://" + cfg.Addr + "/api/info"); err == nil {
		t.Error("still serving after Close")
	}
}
