package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"mirror-proxy/internal/admin"
	"mirror-proxy/internal/auth"
	"mirror-proxy/internal/config"
	"mirror-proxy/internal/middleware"
	"mirror-proxy/internal/proxy"
)

func main() {
	cfg, err := config.Load("config.json")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

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

	// 管理后台
	adminAuth := middleware.BasicAuth(cfg.AdminUser, cfg.AdminPass)
	mux.Handle(adminPath+"/", adminAuth(http.StripPrefix(adminPath, http.FileServer(http.Dir("web/static")))))

	// 管理API
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

	os.MkdirAll("web/static", 0755)

	handler := middleware.CORS(mux)

	fmt.Printf("Mirror Proxy Server starting on %s\n", cfg.ListenAddr)
	fmt.Printf("Admin panel: http://localhost%s%s/\n", cfg.ListenAddr, adminPath)
	fmt.Printf("Admin user: %s\n", cfg.AdminUser)
	fmt.Printf("Docker Hub:  http://localhost%s/{linkID}/{token}/v2/...\n", cfg.ListenAddr)
	fmt.Printf("GHCR:        http://localhost%s/{linkID}/{token}/v2/...\n", cfg.ListenAddr)
	fmt.Printf("GitHub:      http://localhost%s/{linkID}/{token}/github/...\n", cfg.ListenAddr)

	log.Fatal(http.ListenAndServe(cfg.ListenAddr, handler))
}
