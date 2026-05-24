package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"mirror-proxy/internal/auth"
)

// DynamicProxy 创建指向任意目标 URL 的反向代理
func DynamicProxy(targetURL string) http.Handler {
	target, err := url.Parse(targetURL)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Invalid target URL", http.StatusBadRequest)
		})
	}

	p := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.Header.Set("Host", target.Host)
			// 删除 Accept-Encoding，防止响应被压缩，便于修改 HTML
			req.Header.Del("Accept-Encoding")
			if req.Header.Get("User-Agent") == "" {
				req.Header.Set("User-Agent", "MirrorProxy/1.0")
			}
			req.Header.Del("X-Forwarded-For")
		},
		ModifyResponse: func(resp *http.Response) error {
			// 从请求上下文中获取 token 前缀
			tokenPrefix := ""
			if resp.Request != nil {
				if prefix, ok := resp.Request.Context().Value(auth.TokenPrefixContextKey).(string); ok {
					tokenPrefix = prefix
				}
			}

			if tokenPrefix == "" {
				return nil
			}

			// 重写 Location header
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", rewriteURL(loc, tokenPrefix))
			}

			// 对 HTML 响应注入 JS 脚本，重写链接
			contentType := resp.Header.Get("Content-Type")
			if strings.Contains(contentType, "text/html") && resp.Body != nil {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					return err
				}
				resp.Body.Close()

				body = injectTokenPrefixScript(body, tokenPrefix)
				resp.Body = io.NopCloser(bytes.NewReader(body))
				resp.ContentLength = int64(len(body))
				resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
			}

			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "Proxy error: %s", err.Error())
		},
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}

	return p
}
