package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

// Config is the top-level structure of the YAML configuration file.
type Config struct {
	Targets []Target `yaml:"targets"`
}

// Target groups operations that belong to a single name (typically an OS
// release). A target may carry file fetches, ISO extracts, or both.
type Target struct {
	Name  string     `yaml:"name"`
	Files []File     `yaml:"files,omitempty"`
	ISO   []ISOEntry `yaml:"iso,omitempty"`
}

// File describes one HTTP fetch: download `URL` to `Dest`, verify with `SHA256`.
type File struct {
	URL    string `yaml:"url"`
	Dest   string `yaml:"dest"`
	SHA256 string `yaml:"sha256"`
}

// ISOEntry describes one extraction: read `Src` out of the local ISO at
// `From` and write it to `Dest`.
type ISOEntry struct {
	From string `yaml:"from"`
	Src  string `yaml:"src"`
	Dest string `yaml:"dest"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

// validate performs minimal structural checks. It does not verify that URLs
// resolve, that destinations are writable, or that sha256 hashes are valid
// hex; those failures surface at execution time with clearer errors.
func validate(cfg *Config) error {
	if len(cfg.Targets) == 0 {
		return fmt.Errorf("no targets defined")
	}

	seen := make(map[string]bool, len(cfg.Targets))
	for i, t := range cfg.Targets {
		if t.Name == "" {
			return fmt.Errorf("targets[%d]: name is required", i)
		}
		if seen[t.Name] {
			return fmt.Errorf("targets[%d]: duplicate name %q", i, t.Name)
		}
		seen[t.Name] = true

		if len(t.Files) == 0 && len(t.ISO) == 0 {
			return fmt.Errorf("target %q: must have at least one of files or iso", t.Name)
		}

		for j, f := range t.Files {
			if f.URL == "" {
				return fmt.Errorf("target %q files[%d]: url is required", t.Name, j)
			}
			if err := checkAbsPath(f.Dest); err != nil {
				return fmt.Errorf("target %q files[%d] dest: %w", t.Name, j, err)
			}
			if f.SHA256 == "" {
				return fmt.Errorf("target %q files[%d]: sha256 is required", t.Name, j)
			}
		}

		for j, e := range t.ISO {
			if err := checkAbsPath(e.From); err != nil {
				return fmt.Errorf("target %q iso[%d] from: %w", t.Name, j, err)
			}
			if e.Src == "" {
				return fmt.Errorf("target %q iso[%d]: src is required", t.Name, j)
			}
			if err := checkAbsPath(e.Dest); err != nil {
				return fmt.Errorf("target %q iso[%d] dest: %w", t.Name, j, err)
			}
		}
	}

	return nil
}

func checkAbsPath(p string) error {
	if p == "" {
		return fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("path must be absolute: %s", p)
	}
	return nil
}
