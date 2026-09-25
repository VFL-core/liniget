package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds the persisted defaults created by `liniget -stp`.
type Config struct {
	Threads      int    `json:"threads"`
	DownloadDir  string `json:"download_dir"`
	SpeedLimit   int64  `json:"speed_limit_bytes_per_sec"` // 0 = unlimited
	OnExisting   string `json:"on_existing"`                // "skip", "overwrite", "rename"
	Retries      int    `json:"retries"`
	TimeoutSecs  int    `json:"timeout_seconds"`
}

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Threads:     4,
		DownloadDir: filepath.Join(home, "Downloads"),
		SpeedLimit:  0,
		OnExisting:  "rename",
		Retries:     3,
		TimeoutSecs: 30,
	}
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "liniget", "config.json"), nil
}

// loadConfig reads the config file, falling back to defaults if it
// doesn't exist yet (e.g. the user hasn't run `liniget -stp`).
func loadConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return defaultConfig(), err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return defaultConfig(), nil
	}
	if err != nil {
		return defaultConfig(), err
	}

	cfg := defaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return defaultConfig(), fmt.Errorf("config file is corrupt (%s): %w", path, err)
	}
	return cfg, nil
}

func saveConfig(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
