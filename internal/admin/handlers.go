package admin

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
	"time"

	"mirror-proxy/internal/auth"
	"mirror-proxy/internal/config"
)

type Handler struct {
	cfg         *config.Config
	onRestart   func()
}

func NewHandler(cfg *config.Config) *Handler {
	return &Handler{cfg: cfg}
}

func (h *Handler) SetOnRestart(fn func()) {
	h.onRestart = fn
}

// GetLinks 获取所有链接
func (h *Handler) GetLinks(w http.ResponseWriter, r *http.Request) {
	links := h.cfg.ListLinks()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(links)
}

// CreateLink 创建新链接
func (h *Handler) CreateLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		RateLimit int    `json:"rate_limit"`
		AuthMode  string `json:"auth_mode"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Type == "" {
		req.Type = "docker"
	}
	if req.RateLimit <= 0 {
		req.RateLimit = 100
	}
	if req.AuthMode == "" {
		req.AuthMode = "dual"
	}

	link := &config.Link{
		ID:        generateID(),
		Name:      req.Name,
		Token:     auth.GenerateToken(),
		Type:      req.Type,
		AuthMode:  req.AuthMode,
		Enabled:   true,
		RateLimit: req.RateLimit,
		CreatedAt: time.Now().Unix(),
	}

	h.cfg.SetLink(link)
	if err := h.cfg.Save(config.GetConfigFilePath()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(link)
}

// UpdateLink 更新链接
func (h *Handler) UpdateLink(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/api/links/"):]

	link, ok := h.cfg.GetLink(id)
	if !ok {
		http.Error(w, "Link not found", http.StatusNotFound)
		return
	}

	var req struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		Enabled   *bool  `json:"enabled,omitempty"`
		RateLimit int    `json:"rate_limit"`
		AuthMode  string `json:"auth_mode"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name != "" {
		link.Name = req.Name
	}
	if req.Type != "" {
		link.Type = req.Type
	}
	if req.Enabled != nil {
		link.Enabled = *req.Enabled
	}
	if req.RateLimit > 0 {
		link.RateLimit = req.RateLimit
	}
	if req.AuthMode != "" {
		link.AuthMode = req.AuthMode
	}

	h.cfg.SetLink(link)
	if err := h.cfg.Save(config.GetConfigFilePath()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(link)
}

// DeleteLink 删除链接
func (h *Handler) DeleteLink(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/api/links/"):]

	_, ok := h.cfg.GetLink(id)
	if !ok {
		http.Error(w, "Link not found", http.StatusNotFound)
		return
	}

	h.cfg.DeleteLink(id)
	if err := h.cfg.Save(config.GetConfigFilePath()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RegenerateToken 重新生成token
func (h *Handler) RegenerateToken(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/api/links/"):len(r.URL.Path)-len("/token")]

	link, ok := h.cfg.GetLink(id)
	if !ok {
		http.Error(w, "Link not found", http.StatusNotFound)
		return
	}

	link.Token = auth.GenerateToken()
	h.cfg.SetLink(link)
	if err := h.cfg.Save(config.GetConfigFilePath()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": link.Token})
}

// GetStats 获取统计信息
func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	links := h.cfg.ListLinks()
	stats := make(map[string]interface{})
	stats["total_links"] = len(links)
	stats["links"] = links

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// GetConfig 获取配置
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"listen_addr":    h.cfg.ListenAddr,
		"admin_path":     h.cfg.AdminPath,
		"admin_user":     h.cfg.AdminUser,
		"docker_enabled": true,
		"ghcr_enabled":   true,
		"github_enabled": true,
	})
}

// UpdateConfig 更新系统配置
func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ListenAddr string `json:"listen_addr"`
		AdminPath  string `json:"admin_path"`
		AdminUser  string `json:"admin_user"`
		AdminPass  string `json:"admin_pass"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	restartRequired := false

	if req.ListenAddr != "" {
		h.cfg.ListenAddr = req.ListenAddr
		restartRequired = true
	}
	if req.AdminPath != "" {
		if !strings.HasPrefix(req.AdminPath, "/") {
			req.AdminPath = "/" + req.AdminPath
		}
		if h.cfg.AdminPath != req.AdminPath {
			h.cfg.AdminPath = req.AdminPath
			restartRequired = true
		}
	}
	if req.AdminUser != "" {
		h.cfg.AdminUser = req.AdminUser
	}
	if req.AdminPass != "" {
		h.cfg.AdminPass = req.AdminPass
	}

	if err := h.cfg.Save(config.GetConfigFilePath()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if restartRequired && h.onRestart != nil {
		go h.onRestart()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"listen_addr":      h.cfg.ListenAddr,
		"admin_path":       h.cfg.AdminPath,
		"admin_user":       h.cfg.AdminUser,
		"restart_required": restartRequired,
	})
}

// LinksHandler 路由分发
func (h *Handler) LinksHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if r.URL.Path == "/api/links" {
			h.GetLinks(w, r)
			return
		}
	case http.MethodPost:
		if r.URL.Path == "/api/links" {
			h.CreateLink(w, r)
			return
		}
	case http.MethodPut:
		if len(r.URL.Path) > len("/api/links/") {
			h.UpdateLink(w, r)
			return
		}
	case http.MethodDelete:
		if len(r.URL.Path) > len("/api/links/") {
			h.DeleteLink(w, r)
			return
		}
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func generateID() string {
	// 生成8位随机ID
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b[i] = charset[idx.Int64()]
	}
	return string(b)
}
