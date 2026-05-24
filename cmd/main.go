package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"mirror-proxy/internal/admin"
	"mirror-proxy/internal/auth"
	"mirror-proxy/internal/config"
	"mirror-proxy/internal/middleware"
	"mirror-proxy/internal/proxy"
)

import (
	"net/url"
)

// extractTargetURL 从请求路径中提取目标 URL
// 支持格式: /https://target.com/path 或 /http://target.com/path 或 /target.com/path
// 如果匹配，修改 r.URL.Path 为目标路径并返回目标 URL
func extractTargetURL(r *http.Request) (string, bool) {
	path := strings.TrimPrefix(r.URL.Path, "/")

	// 完整 URL 格式: https://... 或 http://...
	if strings.HasPrefix(path, "https://") {
		u, err := url.Parse(path)
		if err == nil {
			r.URL.Path = u.Path
			if u.RawQuery != "" {
				r.URL.RawQuery = u.RawQuery
			}
			return path, true
		}
	}
	if strings.HasPrefix(path, "http://") {
		u, err := url.Parse(path)
		if err == nil {
			r.URL.Path = u.Path
			if u.RawQuery != "" {
				r.URL.RawQuery = u.RawQuery
			}
			return path, true
		}
	}

	// 浏览器规范化后的格式: https:/... 或 http:/...
	if strings.HasPrefix(path, "https:/") {
		targetURL := "https://" + strings.TrimPrefix(path, "https:/")
		u, err := url.Parse(targetURL)
		if err == nil {
			r.URL.Path = u.Path
			if u.RawQuery != "" {
				r.URL.RawQuery = u.RawQuery
			}
			return targetURL, true
		}
	}
	if strings.HasPrefix(path, "http:/") {
		targetURL := "http://" + strings.TrimPrefix(path, "http:/")
		u, err := url.Parse(targetURL)
		if err == nil {
			r.URL.Path = u.Path
			if u.RawQuery != "" {
				r.URL.RawQuery = u.RawQuery
			}
			return targetURL, true
		}
	}

	// 无协议前缀的域名
	if strings.HasPrefix(path, "github.com/") ||
		strings.HasPrefix(path, "raw.githubusercontent.com/") ||
		strings.HasPrefix(path, "api.github.com/") {
		targetURL := "https://" + path
		u, err := url.Parse(targetURL)
		if err == nil {
			r.URL.Path = u.Path
			if u.RawQuery != "" {
				r.URL.RawQuery = u.RawQuery
			}
			return targetURL, true
		}
	}

	return "", false
}

func buildHandler(cfg *config.Config) (*admin.Handler, http.Handler) {
	rl := auth.NewRateLimiter()
	adminHandler := admin.NewHandler(cfg)

	dockerProxy := proxy.NewDockerHubProxy()
	ghcrProxy := proxy.NewGHCRProxy()
	githubProxy := proxy.NewGitHubProxy()
	githubRawProxy := proxy.NewGitHubRawProxy()
	githubAPIProxy := proxy.NewGitHubAPIProxy()

	mux := http.NewServeMux()

	adminPath := cfg.AdminPath
	if adminPath == "" {
		adminPath = "/admin"
	}

	adminAuth := middleware.SessionAuth(adminHandler.SessionStore(), adminPath+"/login")

	// 登录页面（免认证）
	mux.HandleFunc(adminPath+"/login", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/static/login.html")
	})

	// 管理后台（需要 session 认证）
	mux.Handle(adminPath+"/", adminAuth(http.StripPrefix(adminPath, http.FileServer(http.Dir("web/static")))))

	// 认证 API（login 免认证，其他需要认证）
	mux.HandleFunc("/api/auth/login", adminHandler.Login)
	mux.HandleFunc("/api/auth/logout", adminAuth(http.HandlerFunc(adminHandler.Logout)).ServeHTTP)
	mux.HandleFunc("/api/auth/me", adminAuth(http.HandlerFunc(adminHandler.GetMe)).ServeHTTP)

	// 管理 API（需要认证）
	mux.HandleFunc("/api/links", adminAuth(http.HandlerFunc(adminHandler.LinksHandler)).ServeHTTP)
	mux.HandleFunc("/api/links/", adminAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			adminHandler.RegenerateToken(w, r)
		} else {
			switch r.Method {
			case http.MethodPut:
				adminHandler.UpdateLink(w, r)
			case http.MethodDelete:
				adminHandler.DeleteLink(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		}
	})).ServeHTTP)
	mux.Handle("/api/stats", adminAuth(http.HandlerFunc(adminHandler.GetStats)))
	mux.HandleFunc("/api/config", adminAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			adminHandler.GetConfig(w, r)
		case http.MethodPut:
			adminHandler.UpdateConfig(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})).ServeHTTP)

	// GitHub 代理（公开路径，不需要 linkID/token）
	mux.Handle("/github/", http.StripPrefix("/github", githubProxy))
	mux.Handle("/raw/", http.StripPrefix("/raw", githubRawProxy))
	mux.Handle("/api.github.com/", http.StripPrefix("/api.github.com", githubAPIProxy))

	// 鉴权代理：/linkID/token/... → 去掉前缀 → 转发给对应上游
	authProxy := auth.ProxyAuthMiddleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		link, ok := r.Context().Value(auth.LinkContextKey).(*config.Link)
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// URL 代理模式: /https://target.com/path 或 /target.com/path
		if targetURL, isURL := extractTargetURL(r); isURL {
			proxy.DynamicProxy(targetURL).ServeHTTP(w, r)
			return
		}

		switch link.Type {
		case "docker":
			dockerProxy.ServeHTTP(w, r)
		case "ghcr":
			if !strings.HasPrefix(r.URL.Path, "/v2/") {
				r.URL.Path = "/v2" + r.URL.Path
			}
			ghcrProxy.ServeHTTP(w, r)
		case "github":
			if !strings.HasPrefix(r.URL.Path, "/github/") {
				r.URL.Path = "/github" + r.URL.Path
			}
			githubProxy.ServeHTTP(w, r)
		default:
			dockerProxy.ServeHTTP(w, r)
		}
	}))

	// 根路径处理
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, adminPath+"/", http.StatusFound)
			return
		}
		authProxy.ServeHTTP(w, r)
	})

	return adminHandler, middleware.CORS(mux)
}

func main() {
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.json"
	}

	restartCh := make(chan struct{}, 1)

	for {
		cfg, err := config.Load(configPath)
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}

		adminHandler, handler := buildHandler(cfg)
		adminHandler.SetOnRestart(func() {
			select {
			case restartCh <- struct{}{}:
			default:
			}
		})

		os.MkdirAll("web/static", 0755)

		srv := &http.Server{
			Addr:    cfg.ListenAddr,
			Handler: handler,
		}

		fmt.Printf("Mirror Proxy Server starting on %s\n", cfg.ListenAddr)
		fmt.Printf("Admin panel: http://localhost%s%s/\n", cfg.ListenAddr, cfg.AdminPath)
		fmt.Printf("Admin user: %s\n", cfg.AdminUser)
		fmt.Printf("Docker Hub:  http://localhost%s/{linkID}/{token}/v2/...\n", cfg.ListenAddr)
		fmt.Printf("GHCR:        http://localhost%s/{linkID}/{token}/v2/...\n", cfg.ListenAddr)
		fmt.Printf("GitHub:      http://localhost%s/{linkID}/{token}/github/...\n", cfg.ListenAddr)

		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("Server error: %v", err)
			}
		}()

		<-restartCh

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}
		cancel()

		log.Println("Server restarting with new configuration...")
	}
}
