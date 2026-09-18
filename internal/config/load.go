package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// Load reads, decodes and validates a configuration file. Defaults are applied
// to the returned value.
func Load(path string) (*Config, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}

	cfg, err := Decode(data)
	if err != nil {
		return nil, err
	}

	if err := Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func readFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	// Reject an oversized file before decoding it.
	if info.Size() > MaxConfigBytes {
		return nil, fmt.Errorf("config: file is larger than %d bytes", MaxConfigBytes)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	defer file.Close()

	// One byte over the limit is enough to detect growth between Stat and Read.
	data, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if len(data) > MaxConfigBytes {
		return nil, fmt.Errorf("config: file is larger than %d bytes", MaxConfigBytes)
	}
	return data, nil
}

// Decode parses one YAML document into a Config. Unknown fields and a second
// document are errors. Defaults are applied; semantic checks are in Validate.
func Decode(data []byte) (*Config, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("config: file is empty")
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("config: file is empty")
		}
		return nil, fmt.Errorf("config: %w", err)
	}

	var extra Config
	switch err := decoder.Decode(&extra); {
	case err == nil:
		return nil, errors.New("config: multiple YAML documents are not allowed")
	case errors.Is(err, io.EOF):
	default:
		return nil, fmt.Errorf("config: %w", err)
	}

	applyDefaults(&cfg)
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if strings.TrimSpace(cfg.Listen) == "" {
		// The security default is loopback. Exposure must be explicit.
		cfg.Listen = DefaultListen
	}
	for i := range cfg.Backends {
		if cfg.Backends[i].ResponseHeaderTimeoutMS == 0 {
			cfg.Backends[i].ResponseHeaderTimeoutMS = DefaultResponseHeaderTimeoutMS
		}
	}
}
