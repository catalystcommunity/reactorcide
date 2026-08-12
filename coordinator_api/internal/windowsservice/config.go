package windowsservice

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	Name        = "reactorcide-worker"
	DisplayName = "Reactorcide Worker"
)

// Config defines the worker command that the Windows service runs.
// Secret values must be stored in files and passed by file path.
type Config struct {
	Arguments   []string          `json:"arguments"`
	Environment map[string]string `json:"environment,omitempty"`
	LogFile     string            `json:"log_file"`
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read service config: %w", err)
	}
	data = []byte(strings.TrimPrefix(string(data), "\ufeff"))
	var config Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("parse service config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) Validate() error {
	if len(config.Arguments) == 0 || config.Arguments[0] != "worker" {
		return errors.New("service config: arguments must start with worker")
	}
	if strings.TrimSpace(config.LogFile) == "" {
		return errors.New("service config: log_file is required")
	}
	for key := range config.Environment {
		if strings.TrimSpace(key) == "" || strings.Contains(key, "=") {
			return fmt.Errorf("service config: invalid environment variable name %q", key)
		}
		if strings.EqualFold(key, "REACTORCIDE_WORKER_ENROLLMENT_TOKEN") {
			return errors.New("service config: use --enrollment-token-file; do not store the enrollment token in environment")
		}
	}
	return nil
}
