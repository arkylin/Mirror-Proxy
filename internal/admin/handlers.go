package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"mirror-proxy/internal/auth"
	"mirror-proxy/internal/config"
)

func normalizeIPs(ips []string) []string {
	var out []string
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip != "" {
			out = append(out, ip)
		}
	}
	return out
}

type Handler struct {
	cfg       *config.Config
	onRestart func()
	sessions  *SessionStore
}

func NewHandler(cfg *config.Config) *Handler {
	return &Handler{
		cfg:      cfg,
		sessions: newSessionStore(),
	}
}

func (h *Handler) SetOnRestart(fn func()) {
	h.onRestart = fn
}

// ---------- Session Management ----------

type Session struct {
	Token    string
	Username string
	Expires  time.Time
}

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func newSessionStore() *SessionStore {
	s := &SessionStore{sessions: make(map[string]*Session)}
	go s.cleanupLoop()
	return s
}

func (s *SessionStore) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	for range ticker.C {
		s.mu.Lock()
		for token, sess := range s.sessions {
			if time.Now().After(sess.Expires) {
				delete(s.sessions, token)
			}
		}
		s.mu.Unlock()
	}
}

func (s *SessionStore) Create(username string, timeoutHours int) *Session {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)

	if timeoutHours <= 0 {
		timeoutHours = 24
	}

	sess := &Session{
		Token:    token,
		Username: username,
		Expires:  time.Now().Add(time.Duration(timeoutHours) * time.Hour),
	}

	s.mu.Lock()
	s.sessions[token] = sess
	s.mu.Unlock()

	return sess
}

func (s *SessionStore) Get(token string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[token]
	if !ok || time.Now().After(sess.Expires) {
		return nil, false
	}
	return sess, true
}

func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

func (h *Handler) SessionStore() *SessionStore {
	return h.sessions
}

// ---------- Auth Handlers ----------

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request"})
		return
	}

	if req.Username != h.cfg.AdminUser || req.Password != h.cfg.AdminPass {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid credentials"})
		return
	}

	sess := h.sessions.Create(req.Username, h.cfg.SessionTimeout)

	maxAge := h.cfg.SessionTimeout * 3600
	if maxAge <= 0 {
		maxAge = 86400
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    sess.Token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"username": sess.Username})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")
	if err == nil {
		h.sessions.Delete(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "not authenticated"})
		return
	}

	sess, ok := h.sessions.Get(cookie.Value)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "session expired"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"username": sess.Username})
}

// ---------- Link Handlers ----------

func (h *Handler) GetLinks(w http.ResponseWriter, r *http.Request) {
	links := h.cfg.ListLinks()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(links)
}

func (h *Handler) CreateLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string   `json:"name"`
		Type       string   `json:"type"`
		RateLimit  int      `json:"rate_limit"`
		AuthMode   string   `json:"auth_mode"`
		AllowedIPs []string `json:"allowed_ips"`
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
		ID:         generateID(),
		Name:       req.Name,
		Token:      auth.GenerateToken(),
		Type:       req.Type,
		AuthMode:   req.AuthMode,
		Enabled:    true,
		RateLimit:  req.RateLimit,
		AllowedIPs: normalizeIPs(req.AllowedIPs),
		CreatedAt:  time.Now().Unix(),
	}

	h.cfg.SetLink(link)
	if err := h.cfg.Save(config.GetConfigFilePath()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(link)
}

func (h *Handler) UpdateLink(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/api/links/"):]

	link, ok := h.cfg.GetLink(id)
	if !ok {
		http.Error(w, "Link not found", http.StatusNotFound)
		return
	}

	var req struct {
		Name       string   `json:"name"`
		Type       string   `json:"type"`
		Enabled    *bool    `json:"enabled,omitempty"`
		RateLimit  int      `json:"rate_limit"`
		AuthMode   string   `json:"auth_mode"`
		AllowedIPs []string `json:"allowed_ips,omitempty"`
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
	if req.AllowedIPs != nil {
		link.AllowedIPs = normalizeIPs(req.AllowedIPs)
	}

	h.cfg.SetLink(link)
	if err := h.cfg.Save(config.GetConfigFilePath()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(link)
}

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

func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	links := h.cfg.ListLinks()
	stats := make(map[string]interface{})
	stats["total_links"] = len(links)
	stats["links"] = links

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"admin_path":      h.cfg.AdminPath,
		"admin_user":      h.cfg.AdminUser,
		"session_timeout": h.cfg.SessionTimeout,
		"docker_enabled":  true,
		"ghcr_enabled":    true,
		"github_enabled":  true,
	})
}

func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AdminPath     string `json:"admin_path"`
		AdminUser     string `json:"admin_user"`
		AdminPass     string `json:"admin_pass"`
		SessionTimeout int   `json:"session_timeout"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	restartRequired := false

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
	if req.SessionTimeout > 0 {
		h.cfg.SessionTimeout = req.SessionTimeout
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
		"admin_path":       h.cfg.AdminPath,
		"admin_user":       h.cfg.AdminUser,
		"session_timeout":  h.cfg.SessionTimeout,
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

// AuthHandler 认证路由分发
func (h *Handler) AuthHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		if r.URL.Path == "/api/auth/login" {
			h.Login(w, r)
			return
		}
		if r.URL.Path == "/api/auth/logout" {
			h.Logout(w, r)
			return
		}
	case http.MethodGet:
		if r.URL.Path == "/api/auth/me" {
			h.GetMe(w, r)
			return
		}
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func generateID() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b[i] = charset[idx.Int64()]
	}
	return string(b)
}
