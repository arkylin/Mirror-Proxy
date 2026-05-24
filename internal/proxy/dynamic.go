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
			saveProxyContext(req)

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
			// 删除上游返回的 HSTS / CSP header，防止浏览器将代理域名标记为
			// HTTPS-only 或自动升级 HTTP URL。
			resp.Header.Del("Strict-Transport-Security")
			resp.Header.Del("Content-Security-Policy")

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

			host, proto := loadProxyInfo(resp.Request)

			// 重写 Location header
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", rewriteURL(loc, tokenPrefix))
			}

			// 对 HTML 响应做服务端 URL 重写 + 注入 JS 兜底
			contentType := resp.Header.Get("Content-Type")
			if strings.Contains(contentType, "text/html") && resp.Body != nil {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					return err
				}
				resp.Body.Close()

				// 1. 服务端重写所有已知 URL 属性（应对 CSP 禁止内联脚本的情况）
				body = rewriteHTMLBody(body, tokenPrefix, host, proto)
				// 2. 注入 JS 处理动态添加的内容（无 CSP 时生效）
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
