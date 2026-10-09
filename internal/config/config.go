// Package config stores user settings and the Dropbox refresh token on disk.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const appDirName = "DropboxUploader"

type Config struct {
	AppKey       string `json:"appKey,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	AccountName  string `json:"accountName,omitempty"`
	AccountEmail string `json:"accountEmail,omitempty"`

	Lang        string `json:"lang,omitempty"`
	Workers     int    `json:"workers"`
	LastLocal   string `json:"lastLocal,omitempty"`
	LastDropbox string `json:"lastDropbox,omitempty"`
	ViewMode    string `json:"viewMode,omitempty"`
	WindowW     int    `json:"windowW,omitempty"`
	WindowH     int    `json:"windowH,omitempty"`
}

type Store struct {
	mu   sync.Mutex
	path string
	cfg  Config
}

// ConfigDir is %APPDATA%\DropboxUploader on Windows (~/Library/Application Support on macOS).
func ConfigDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, appDirName)
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

// DataDir is %LOCALAPPDATA%\DropboxUploader on Windows (~/Library/Caches on macOS);
// it holds the queue, the thumbnail cache and logs.
func DataDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, appDirName)
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

func Load() *Store {
	s := &Store{path: filepath.Join(ConfigDir(), "config.json")}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.cfg)
	}
	if s.cfg.Workers < 1 || s.cfg.Workers > 8 {
		s.cfg.Workers = 4
	}
	if s.cfg.Lang == "" {
		s.cfg.Lang = "ca"
	}
	if s.cfg.ViewMode == "" {
		s.cfg.ViewMode = "grid"
	}
	return s
}

func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Update applies fn to the config and persists it.
func (s *Store) Update(fn func(c *Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.cfg)
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
