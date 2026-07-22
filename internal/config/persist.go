package config

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Persistence wraps Config with thread-safe file I/O.
type Persistence struct {
	mu   sync.RWMutex
	cfg  Config
	path string
	log  *slog.Logger
}

// SetLogger sets the logger used for error reporting in Save operations.
// Must be called before any write operations; safe for concurrent use.
func (p *Persistence) SetLogger(logger *slog.Logger) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log = logger
}

// LoadOrCreate reads config from path, creating defaults if absent.
func LoadOrCreate(path string) (*Persistence, error) {
	p := &Persistence{path: path}
	if err := p.reload(); err != nil {
		return nil, err
	}
	return p, nil
}

// Get returns a copy of the current config.
func (p *Persistence) Get() Config {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg
}

// Update merges partial config and persists to disk.
// The callback runs under the write lock. If it returns an error, the save is aborted.
func (p *Persistence) Update(fn func(cfg *Config) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := fn(&p.cfg); err != nil {
		return err
	}
	return p.save()
}

// reload reads the file, falling back to defaults if absent.
func (p *Persistence) reload() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	cfg, err := readConfigFile(p.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log := p.log
			if log == nil {
				log = slog.Default()
			}
			log.Error("read config failed", "path", p.path, "error", err, "component", "config")
		}
		return err
	}

	// Auth bootstrapping: auto-generate API key and hash password on first load.
	needsSave := false
	if cfg.Auth.Method != "" && cfg.Auth.Method != "none" && cfg.Auth.APIKey == "" {
		cfg.Auth.APIKey = GenerateAPIKey()
		needsSave = true
	}
	if cfg.Auth.Password != "" && !strings.HasPrefix(cfg.Auth.Password, "$2") {
		hashed, hashErr := HashPassword(cfg.Auth.Password)
		if hashErr == nil {
			cfg.Auth.Password = hashed
			needsSave = true
		}
	}

	p.cfg = cfg

	// If the file didn't exist, readConfigFile returned defaults — persist them.
	if _, statErr := os.Stat(p.path); os.IsNotExist(statErr) || needsSave {
		return p.save()
	}

	return nil
}

func (p *Persistence) save() error {
	data, err := json.MarshalIndent(p.cfg, "", "  ")
	if err != nil {
		if p.log != nil {
			p.log.Error("marshal config failed", "error", err, "component", "config")
		}
		return err
	}
	if err := os.WriteFile(p.path, data, 0600); err != nil {
		if p.log != nil {
			p.log.Error("write config failed", "path", p.path, "error", err, "component", "config")
		}
		return err
	}
	return nil
}
