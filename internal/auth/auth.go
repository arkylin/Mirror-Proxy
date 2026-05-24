package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"mirror-proxy/internal/config"
)

type RateLimiter struct {
	visitors map[string]*visitor
	mu       sync.RWMutex
}

type visitor struct {
	count    int
	lastSeen time.Time
}

func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{visitors: make(map[string]*visitor)}
	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(time.Minute)
	for range ticker.C {
		rl.mu.Lock()
		for ip, v := range rl.visitors {
			if time.Since(v.lastSeen) > time.Minute {
				delete(rl.visitors, ip)
			}
		}
		rl.mu.Unlock()
	}
}

func (rl *RateLimiter) Allow(ip string, limit int) bool {
	if limit <= 0 {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, exists := rl.visitors[ip]
	if !exists {
		rl.visitors[ip] = &visitor{count: 1, lastSeen: time.Now()}
		return true
	}

	if time.Since(v.lastSeen) > time.Minute {
		v.count = 1
		v.lastSeen = time.Now()
		return true
	}

	if v.count >= limit {
		return false
	}

	v.count++
	v.lastSeen = time.Now()
	return true
}

func GenerateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func getAuthMode(link *config.Link) string {
	if link.AuthMode == "" {
		return "dual"
	}
	return link.AuthMode
}

// CheckIPAllowed 检查客户端 IP 是否在链接的白名单中
func CheckIPAllowed(clientIP string, allowedIPs []string) bool {
	if len(allowedIPs) == 0 {
		return true
	}

	ip := net.ParseIP(clientIP)
	if ip == nil {
		// 尝试去掉端口
		host, _, err := net.SplitHostPort(clientIP)
		if err == nil {
			ip = net.ParseIP(host)
		}
		if ip == nil {
			return false
		}
	}

	for _, allowed := range allowedIPs {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}

		// CIDR 格式
		if strings.Contains(allowed, "/") {
			_, ipNet, err := net.ParseCIDR(allowed)
			if err == nil && ipNet.Contains(ip) {
				return true
			}
			continue
		}

		// 单个 IP
		allowedIP := net.ParseIP(allowed)
		if allowedIP != nil && allowedIP.Equal(ip) {
			return true
		}
	}

	return false
}

// extractClientIP 从请求中提取客户端 IP（去掉端口）
func extractClientIP(r *http.Request) string {
	ip := r.RemoteAddr
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		ip = strings.Split(xf, ",")[0]
	} else if xr := r.Header.Get("X-Real-Ip"); xr != "" {
		ip = xr
	}
	ip = strings.TrimSpace(ip)
	host, _, err := net.SplitHostPort(ip)
	if err == nil {
		ip = host
	}
	return ip
}

func ValidateLinkToken(linkID, token string) (*config.Link, bool) {
	cfg := config.Get()
	link, ok := cfg.GetLink(linkID)
	if !ok {
		return nil, false
	}
	if !link.Enabled {
		return nil, false
	}
	if getAuthMode(link) != "dual" {
		return nil, false
	}
	if link.Token != token {
		return nil, false
	}
	return link, true
}

func ValidateLinkTokenSingle(token string) (*config.Link, bool) {
	cfg := config.Get()
	link, ok := cfg.GetLinkByToken(token)
	if !ok {
		return nil, false
	}
	if !link.Enabled {
		return nil, false
	}
	if getAuthMode(link) != "single" {
		return nil, false
	}
	return link, true
}

type contextKey string

const LinkContextKey contextKey = "link"
const TokenPrefixContextKey contextKey = "tokenPrefix"

func ProxyAuthMiddleware(rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimPrefix(r.URL.Path, "/")
			parts := strings.SplitN(path, "/", 3)
			if len(parts) < 1 {
				http.Error(w, "Unauthorized: missing token", http.StatusUnauthorized)
				return
			}

			var link *config.Link
			var ok bool
			var newPath string
			var tokenPrefix string

			// 优先尝试 dual 模式 (/{linkID}/{token}/...)
			if len(parts) >= 2 {
				link, ok = ValidateLinkToken(parts[0], parts[1])
				if ok {
					tokenPrefix = "/" + parts[0] + "/" + parts[1]
					if len(parts) == 2 {
						newPath = "/"
					} else {
						newPath = "/" + parts[2]
					}
				}
			}

			// 尝试 single 模式 (/{token}/...)
			if !ok {
				link, ok = ValidateLinkTokenSingle(parts[0])
				if ok {
					tokenPrefix = "/" + parts[0]
					if len(parts) == 1 {
						newPath = "/"
					} else {
						newPath = "/" + strings.Join(parts[1:], "/")
					}
				}
			}

			if !ok {
				http.Error(w, "Unauthorized: invalid link or token", http.StatusUnauthorized)
				return
			}

			clientIP := extractClientIP(r)

			if !CheckIPAllowed(clientIP, link.AllowedIPs) {
				http.Error(w, "Forbidden: IP not allowed", http.StatusForbidden)
				return
			}

			if !rl.Allow(clientIP, link.RateLimit) {
				http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
				return
			}

			r.URL.Path = newPath

			// 存储link和token前缀到context
			ctx := context.WithValue(r.Context(), LinkContextKey, link)
			ctx = context.WithValue(ctx, TokenPrefixContextKey, tokenPrefix)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
