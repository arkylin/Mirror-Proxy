package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// NewGHCRProxy 创建 GHCR 反向代理
func NewGHCRProxy() http.Handler {
	target, _ := url.Parse("https://ghcr.io")

	p := httputil.NewSingleHostReverseProxy(target)
	p.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		req.Header.Set("Host", target.Host)
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", "Docker-Client/24.0.0")
		}
		req.Header.Del("X-Forwarded-For")
	}

	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"errors":[{"code":"PROXY_ERROR","message":"%s"}]}`, err.Error())
	}

	p.Transport = &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	return p
}
