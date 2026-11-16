package test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"grok-quality-guard/internal/app"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
)

func TestUpdateStatePathPersistsPointerAcrossRestart(t *testing.T) {
	root := t.TempDir()
	bootstrapPath := filepath.Join(root, "bootstrap", "state.json")
	movedPath := filepath.Join(root, "moved", "state.json")
	cfg := appconfig.DefaultConfig()
	cfg.Enabled = false
	cfg.StateFile = bootstrapPath
	rawConfig, errMarshal := json.Marshal(cfg)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	first := app.NewPlugin()
	if errConfigure := first.Configure(rawConfig); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	t.Cleanup(first.Shutdown)
	activePath, errMove := first.UpdateStatePath(movedPath)
	if errMove != nil {
		t.Fatal(errMove)
	}
	if activePath != movedPath {
		t.Fatalf("active path = %q, want %q", activePath, movedPath)
	}
	if _, errStat := os.Stat(movedPath); errStat != nil {
		t.Fatalf("moved state file not found: %v", errStat)
	}
	pointerPath, errPointer := appstate.ReadPathPointer(bootstrapPath)
	if errPointer != nil {
		t.Fatal(errPointer)
	}
	if pointerPath != movedPath {
		t.Fatalf("state path pointer = %q, want %q", pointerPath, movedPath)
	}

	second := app.NewPlugin()
	if errConfigure := second.Configure(rawConfig); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	t.Cleanup(second.Shutdown)
	reloadedPath, errReload := second.UpdateStatePath(movedPath)
	if errReload != nil {
		t.Fatalf("pointer target was not loaded after restart: %v", errReload)
	}
	if reloadedPath != movedPath {
		t.Fatalf("reloaded active path = %q, want %q", reloadedPath, movedPath)
	}
}

func TestUpdateStatePathRejectsExistingTarget(t *testing.T) {
	root := t.TempDir()
	bootstrapPath := filepath.Join(root, "bootstrap", "state.json")
	targetPath := filepath.Join(root, "target", "state.json")
	cfg := appconfig.DefaultConfig()
	cfg.Enabled = false
	cfg.StateFile = bootstrapPath
	rawConfig, errMarshal := json.Marshal(cfg)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin := app.NewPlugin()
	if errConfigure := plugin.Configure(rawConfig); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	t.Cleanup(plugin.Shutdown)
	if errMkdir := os.MkdirAll(filepath.Dir(targetPath), 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	if errWrite := os.WriteFile(targetPath, []byte("existing"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, errMove := plugin.UpdateStatePath(targetPath); !errors.Is(errMove, os.ErrExist) {
		t.Fatalf("UpdateStatePath() error = %v, want os.ErrExist", errMove)
	}
	if _, errStat := os.Stat(bootstrapPath); errStat != nil {
		t.Fatalf("bootstrap state file missing after rejected move: %v", errStat)
	}
}
