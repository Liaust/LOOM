package ui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const StateSchemaVersion = "loom.cli_state.v0.2"

type State struct {
	SchemaVersion string         `json:"schema_version"`
	Theme         string         `json:"theme"`
	LastScreen    string         `json:"last_screen"`
	RecentActions []RecentAction `json:"recent_actions"`
}

type RecentAction struct {
	ActionID string    `json:"action_id"`
	UsedAt   time.Time `json:"used_at"`
}

func DefaultState() State {
	return State{
		SchemaVersion: StateSchemaVersion,
		Theme:         ThemeCoffee,
		LastScreen:    "home",
		RecentActions: []RecentAction{},
	}
}

func StatePath(env Env) string {
	if xdg := strings.TrimSpace(env.Get("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "loom", "cli-state.json")
	}
	home := strings.TrimSpace(env.Get("HOME"))
	if home == "" {
		if detected, err := os.UserHomeDir(); err == nil {
			home = detected
		}
	}
	if home == "" {
		return filepath.Join(".loom", "cli-state.json")
	}
	return filepath.Join(home, ".config", "loom", "cli-state.json")
}

func LoadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultState(), nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.SchemaVersion == "" {
		state.SchemaVersion = StateSchemaVersion
	}
	if state.Theme == "" {
		state.Theme = ThemeCoffee
	}
	if state.LastScreen == "" {
		state.LastScreen = "home"
	}
	if state.RecentActions == nil {
		state.RecentActions = []RecentAction{}
	}
	return state, nil
}

func LoadStateBestEffort(path string) State {
	state, err := LoadState(path)
	if err != nil {
		return DefaultState()
	}
	return state
}

func SaveState(path string, state State) error {
	if state.SchemaVersion == "" {
		state.SchemaVersion = StateSchemaVersion
	}
	if state.Theme == "" {
		state.Theme = ThemeCoffee
	}
	if state.RecentActions == nil {
		state.RecentActions = []RecentAction{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}
