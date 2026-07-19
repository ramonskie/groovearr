package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Persistence wraps Config with thread-safe file I/O.
type Persistence struct {
	mu   sync.RWMutex
	cfg  Config
	path string
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
		return err
	}

	// If the file didn't exist, readConfigFile returned defaults — persist them.
	if _, statErr := os.Stat(p.path); os.IsNotExist(statErr) {
		p.cfg = cfg
		return p.save()
	}

	p.cfg = cfg
	return nil
}

func (p *Persistence) save() error {
	data, err := json.MarshalIndent(p.cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.path, data, 0600)
}
