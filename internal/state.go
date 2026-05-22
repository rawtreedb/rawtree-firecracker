package observability

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const DefaultStateDir = "/tmp/rawtree-firecracker/sandboxes"

func StateDir() string {
	if value := os.Getenv("RAWTREE_SANDBOX_STATE_DIR"); value != "" {
		return value
	}
	return DefaultStateDir
}

func StatePath(sandboxID string) string {
	return filepath.Join(StateDir(), sandboxID+".json")
}

func WriteState(path string, state SandboxState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func ReadState(sandboxID string) (SandboxState, error) {
	return ReadStateFile(StatePath(sandboxID))
}

func ReadStateFile(path string) (SandboxState, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return SandboxState{}, fmt.Errorf("read sandbox state %s: %w", path, err)
	}

	state := SandboxState{}
	if err := json.Unmarshal(content, &state); err != nil {
		return SandboxState{}, fmt.Errorf("parse sandbox state %s: %w", path, err)
	}
	return state, nil
}
