package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds the application configuration.
type Config struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Region       string `json:"region,omitempty"` // US (default), EMEA, or AUS
}

// Dir returns the apsnav config directory path (~/.config/apsnav), creating it if needed.
// We use ~/.config on all platforms for consistency across macOS, Linux, and Windows.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "apsnav")
	return dir, os.MkdirAll(dir, 0700)
}

// Path returns the path to the config file without creating any directories.
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "~/.config/apsnav/config.json"
	}
	return filepath.Join(home, ".config", "apsnav", "config.json")
}

// Load reads the ClientID from APS_CLIENT_ID env var first, then
// ~/.config/apsnav/config.json. Returns an error with instructions if neither exists.
func Load() (*Config, error) {
	if id := os.Getenv("APS_CLIENT_ID"); id != "" {
		return &Config{
			ClientID:     id,
			ClientSecret: os.Getenv("APS_CLIENT_SECRET"),
			Region:       os.Getenv("APS_REGION"),
		}, nil
	}

	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.json")

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf(
			"APS_CLIENT_ID env var not set and no config found.\n"+
				"Create %s with:\n"+
				"  {\"client_id\": \"<id>\", \"client_secret\": \"<secret>\"}\n\n"+
				"Or set env vars: APS_CLIENT_ID and APS_CLIENT_SECRET\n\n"+
				"Register at: https://aps.autodesk.com/myapps\n"+
				"Redirect URI: http://localhost:7879/callback",
			path,
		)
	}
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("client_id is empty in %s", path)
	}
	return &cfg, nil
}
