package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// NewGitHubProxy 创建 GitHub 主站反向代理
func NewGitHubProxy() http.Handler {
	target, _ := url.Parse("https://github.com")

	p := httputil.NewSingleHostReverseProxy(target)
	p.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		req.Header.Set("Host", target.Host)
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", "MirrorProxy/1.0")
		}
		req.Header.Del("X-Forwarded-For")
	}

	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "GitHub proxy error: %s", err.Error())
	}

	p.Transport = &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	return p
}

// NewGitHubRawProxy 创建 GitHub Raw 反向代理
func NewGitHubRawProxy() http.Handler {
	target, _ := url.Parse("https://raw.githubusercontent.com")

	p := httputil.NewSingleHostReverseProxy(target)
	p.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		req.Header.Set("Host", target.Host)
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", "MirrorProxy/1.0")
		}
		req.Header.Del("X-Forwarded-For")
	}

	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "GitHub raw proxy error: %s", err.Error())
	}

	p.Transport = &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	return p
}

// NewGitHubAPIProxy 创建 GitHub API 反向代理
func NewGitHubAPIProxy() http.Handler {
	target, _ := url.Parse("https://api.github.com")

	p := httputil.NewSingleHostReverseProxy(target)
	p.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		req.Header.Set("Host", target.Host)
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", "MirrorProxy/1.0")
		}
		req.Header.Del("X-Forwarded-For")
	}

	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"message":"GitHub API proxy error: %s"}`, err.Error())
	}

	p.Transport = &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	return p
}
