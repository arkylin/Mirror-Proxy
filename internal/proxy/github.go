package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"mirror-proxy/internal/auth"
)

// context keys for preserving original request info through the proxy
type proxyHostKey string
const proxyHostCtxKey proxyHostKey = "proxyHost"

type proxyProtoKey string
const proxyProtoCtxKey proxyProtoKey = "proxyProto"

// saveProxyContext saves the original request host and protocol into the
// request context before Director mutates req.Host / req.URL.
func saveProxyContext(req *http.Request) {
	host := req.Host
	proto := "http"
	if req.TLS != nil {
		proto = "https"
	}
	if fp := req.Header.Get("X-Forwarded-Proto"); fp != "" {
		proto = fp
	}
	ctx := context.WithValue(req.Context(), proxyHostCtxKey, host)
	ctx = context.WithValue(ctx, proxyProtoCtxKey, proto)
	*req = *req.WithContext(ctx)
}

// loadProxyInfo reads the original host/protocol back from the outbound
// request's context (injected by saveProxyContext in Director).
func loadProxyInfo(req *http.Request) (host, proto string) {
	if req == nil {
		return "", "http"
	}
	if h, ok := req.Context().Value(proxyHostCtxKey).(string); ok {
		host = h
	}
	if p, ok := req.Context().Value(proxyProtoCtxKey).(string); ok {
		proto = p
	}
	return
}

// NewGitHubProxy 创建 GitHub 主站反向代理
func NewGitHubProxy() http.Handler {
	target, _ := url.Parse("https://github.com")

	p := httputil.NewSingleHostReverseProxy(target)
	p.Director = func(req *http.Request) {
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
	}

	p.ModifyResponse = func(resp *http.Response) error {
		// 从请求上下文中获取 token 前缀
		tokenPrefix := ""
		if resp.Request != nil {
			if prefix, ok := resp.Request.Context().Value(auth.TokenPrefixContextKey).(string); ok {
				tokenPrefix = prefix
			}
		}

		// 删除上游返回的 HSTS / CSP header，防止浏览器将代理域名标记为
		// HTTPS-only 或自动升级 HTTP URL。
		resp.Header.Del("Strict-Transport-Security")
		resp.Header.Del("Content-Security-Policy")

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

// rewriteURL 重写 URL，在前面加上 token 前缀
func rewriteURL(u string, prefix string) string {
	if u == "" || prefix == "" {
		return u
	}
	// 已经是完整 URL（带 scheme）
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	// 已经是带前缀的路径
	if strings.HasPrefix(u, prefix+"/") || u == prefix {
		return u
	}
	// 以 / 开头的绝对路径，加上前缀
	if strings.HasPrefix(u, "/") {
		return prefix + u
	}
	// 相对路径，不做处理
	return u
}

// injectTokenPrefixScript 在 HTML 中注入 JS 脚本，重写所有链接使其包含 token 前缀
func injectTokenPrefixScript(body []byte, prefix string) []byte {
	script := []byte(`<script>` +
		`(function(){` +
		`var p='` + prefix + `';` +
		`function rw(v){return v&&v.startsWith('/')&&!v.startsWith(p+'/')&&v!==p?p+v:v};` +
		`function fix(root){` +
		`root.querySelectorAll&&root.querySelectorAll('a[href]').forEach(function(a){a.href=rw(a.getAttribute('href'))});` +
		`root.querySelectorAll&&root.querySelectorAll('form[action]').forEach(function(f){f.action=rw(f.getAttribute('action'))});` +
		`root.querySelectorAll&&root.querySelectorAll('[src]').forEach(function(el){var s=el.getAttribute('src');if(s&&s.startsWith('/')&&!s.startsWith(p+'/'))el.setAttribute('src',p+s)});` +
		`}` +
		`fix(document);` +
		`if(window.MutationObserver){` +
		`new MutationObserver(function(ms){ms.forEach(function(m){m.addedNodes.forEach(function(n){if(n.nodeType===1)fix(n)})})}).observe(document.documentElement||document.body,{childList:true,subtree:true});` +
		`}` +
		`document.addEventListener('click',function(e){` +
		`var a=e.target.closest('a');` +
		`if(!a)return;` +
		`var h=a.getAttribute('href');` +
		`if(h&&h.startsWith('/')&&!h.startsWith(p+'/')&&h!==p){` +
		`e.preventDefault();` +
		`location.href=p+h;` +
		`}` +
		`},true);` +
		`document.addEventListener('submit',function(e){` +
		`var f=e.target.closest('form');` +
		`if(!f)return;` +
		`var h=f.getAttribute('action');` +
		`if(h&&h.startsWith('/')&&!h.startsWith(p+'/')&&h!==p){` +
		`f.setAttribute('action',p+h);` +
		`}` +
		`},true);` +
		`})();` +
		`</script>`)

	// 尝试在 </head> 前插入
	if idx := bytes.Index(body, []byte("</head>")); idx != -1 {
		return append(body[:idx], append(script, body[idx:]...)...)
	}
	// 或者在 <body> 标签后插入
	if idx := bytes.Index(body, []byte("<body")); idx != -1 {
		endIdx := bytes.Index(body[idx:], []byte(">"))
		if endIdx != -1 {
			pos := idx + endIdx + 1
			return append(body[:pos], append(script, body[pos:]...)...)
		}
	}
	// fallback：在文档开头插入
	return append(script, body...)
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

	p.ModifyResponse = func(resp *http.Response) error {
		tokenPrefix := ""
		if resp.Request != nil {
			if prefix, ok := resp.Request.Context().Value(auth.TokenPrefixContextKey).(string); ok {
				tokenPrefix = prefix
			}
		}
		if tokenPrefix != "" {
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", rewriteURL(loc, tokenPrefix))
			}
		}
		return nil
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

	p.ModifyResponse = func(resp *http.Response) error {
		tokenPrefix := ""
		if resp.Request != nil {
			if prefix, ok := resp.Request.Context().Value(auth.TokenPrefixContextKey).(string); ok {
				tokenPrefix = prefix
			}
		}
		if tokenPrefix != "" {
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", rewriteURL(loc, tokenPrefix))
			}
		}
		return nil
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
