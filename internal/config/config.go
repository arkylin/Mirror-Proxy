package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

var configFilePath string = "config.json"

func SetConfigFilePath(path string) {
	configFilePath = path
}

func GetConfigFilePath() string {
	return configFilePath
}

type Config struct {
	ListenAddr       string           `json:"listen_addr"`
	AdminPath        string           `json:"admin_path"`
	AdminUser        string           `json:"admin_user"`
	AdminPass        string           `json:"admin_pass"`
	SessionTimeout   int              `json:"session_timeout"` // hours
	DockerHubHost    string           `json:"docker_hub_host"`
	GHCRCacheEnabled bool             `json:"ghcr_cache_enabled"`
	CacheDir         string           `json:"cache_dir"`
	MaxCacheSize     int64            `json:"max_cache_size"`
	Links            map[string]*Link `json:"links"`
	mu               sync.RWMutex
}

type Link struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Token       string   `json:"token"`
	Type        string   `json:"type"` // docker, ghcr, github
	AuthMode    string   `json:"auth_mode"` // single, dual
	Enabled     bool     `json:"enabled"`
	RateLimit   int      `json:"rate_limit"` // requests per minute
	AllowedIPs  []string `json:"allowed_ips"` // empty = allow all
	CreatedAt   int64    `json:"created_at"`
	AccessCount int64    `json:"access_count"`
	LastAccess  int64    `json:"last_access"`
}

var (
	cfg  *Config
	once sync.Once
)

func Load(path string) (*Config, error) {
	configFilePath = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg = defaultConfig()
			return cfg, cfg.Save(path)
		}
		return nil, err
	}
	cfg = defaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if cfg.Links == nil {
		cfg.Links = make(map[string]*Link)
	}
	return cfg, nil
}

func defaultConfig() *Config {
	return &Config{
		ListenAddr:       ":8080",
		AdminPath:        "/admin",
		AdminUser:        "admin",
		AdminPass:        "admin123",
		SessionTimeout:   24,
		DockerHubHost:    "registry-1.docker.io",
		GHCRCacheEnabled: true,
		CacheDir:         "./cache",
		MaxCacheSize:     10 * 1024 * 1024 * 1024, // 10GB
		Links:            make(map[string]*Link),
	}
}

func (c *Config) Save(path string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (c *Config) GetLink(id string) (*Link, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	link, ok := c.Links[id]
	return link, ok
}

func (c *Config) GetLinkByToken(token string) (*Link, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, link := range c.Links {
		if link.Token == token {
			return link, true
		}
	}
	return nil, false
}

func (c *Config) SetLink(link *Link) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Links[link.ID] = link
}

func (c *Config) DeleteLink(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.Links, id)
}

func (c *Config) ListLinks() []*Link {
	c.mu.RLock()
	defer c.mu.RUnlock()
	links := make([]*Link, 0, len(c.Links))
	for _, l := range c.Links {
		links = append(links, l)
	}
	return links
}

func Get() *Config {
	return cfg
}
