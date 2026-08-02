package mcpconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/internal/privatefile"
)

const (
	SchemaVersion = 1
	maxConfigSize = 16 << 10
)

// Config contains the local daemon credential shared by all MCP clients.
type Config struct {
	SchemaVersion int    `json:"schema_version"`
	ControlURL    string `json:"control_url"`
	Token         string `json:"token"`
}

func New(controlURL, token string) Config {
	return Config{
		SchemaVersion: SchemaVersion,
		ControlURL:    controlURL,
		Token:         token,
	}
}

func (config Config) Validate() error {
	if config.SchemaVersion != SchemaVersion {
		return errors.New("unsupported MCP client configuration schema")
	}
	if _, err := controlplane.NewHTTPClient(config.ControlURL, config.Token); err != nil {
		return fmt.Errorf("invalid MCP client configuration: %w", err)
	}
	return nil
}

func Load(path string) (Config, error) {
	var config Config
	if err := privatefile.ReadJSON(path, maxConfigSize, &config); err != nil {
		return Config{}, err
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func Write(path string, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	return privatefile.WriteJSON(path, config)
}

func DefaultDirectory() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration: %w", err)
	}
	return filepath.Join(root, "rin"), nil
}
