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
			resp.Header.Del("Transfer-Encoding")
		}

		// 确保所有响应都带上 CORS 头
		resp.Header.Set("Access-Control-Allow-Origin", "*")
		resp.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD, PATCH")
		resp.Header.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Origin, X-Requested-With")

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

var githubHosts = []string{
	"github.com",
	"www.github.com",
	"raw.githubusercontent.com",
	"api.github.com",
	"codeload.github.com",
	"objects.githubusercontent.com",
	"user-images.githubusercontent.com",
	"camo.githubusercontent.com",
	"avatars.githubusercontent.com",
}

func isGitHubURL(u string) bool {
	for _, h := range githubHosts {
		if strings.HasPrefix(u, "https://"+h+"/") || strings.HasPrefix(u, "http://"+h+"/") {
			return true
		}
	}
	return false
}

// rewriteURL 重写 URL，在前面加上 token 前缀
func rewriteURL(u string, prefix string) string {
	if u == "" || prefix == "" {
		return u
	}
	// 已经是带前缀的路径
	if strings.HasPrefix(u, prefix+"/") || u == prefix {
		return u
	}
	// GitHub 相关的绝对 URL，添加前缀代理
	if isGitHubURL(u) {
		return prefix + "/" + u
	}
	// 以 / 开头的绝对路径，加上前缀
	if strings.HasPrefix(u, "/") {
		return prefix + u
	}
	// 相对路径，不做处理
	return u
}

// injectTokenPrefixScript 在 HTML 中注入 JS 脚本，重写所有链接使其包含 token 前缀
// 同时拦截 fetch / XMLHttpRequest / EventSource / WebSocket，将直接访问 github.com
// 的请求也转回代理，从而彻底避免 CORS 问题。
func injectTokenPrefixScript(body []byte, prefix string) []byte {
	script := []byte(`<script>` +
		`(function(){` +
		`var p='` + prefix + `';` +
		`if(typeof crypto!=='undefined'&&!crypto.randomUUID){crypto.randomUUID=function(){return'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g,function(c){var r=Math.random()*16|0,v=c==='x'?r:(r&0x3|0x8);return v.toString(16);});};}` +
		`var gh=['https://github.com/','https://www.github.com/','https://raw.githubusercontent.com/','https://api.github.com/','https://codeload.github.com/','https://objects.githubusercontent.com/','https://user-images.githubusercontent.com/','https://camo.githubusercontent.com/','https://avatars.githubusercontent.com/','http://github.com/','http://www.github.com/','http://raw.githubusercontent.com/','http://api.github.com/','http://codeload.github.com/','http://objects.githubusercontent.com/','http://user-images.githubusercontent.com/','http://camo.githubusercontent.com/','http://avatars.githubusercontent.com/','wss://github.com/','wss://raw.githubusercontent.com/','wss://api.github.com/'];` +
		`function rw(v){` +
		`if(!v||v===p)return v;` +
		`if(v.startsWith(p+'/'))return v;` +
		`for(var i=0;i<gh.length;i++){if(v.startsWith(gh[i]))return p+'/'+v;}` +
		`if(v.startsWith('/'))return p+v;` +
		`return v;` +
		`}` +
		`function rfw(u){` +
		`if(typeof u!=='string'){` +
		`if(u&&typeof u==='object'){` +
		`if(typeof u.url==='string'){var r=rfw(u.url);if(r!==u.url){try{var q={};['method','headers','body','mode','credentials','cache','redirect','referrer','referrerPolicy','integrity','keepalive','signal'].forEach(function(k){if(k in u)q[k]=u[k]});return new Request(r,q);}catch(e){return r;}}return u;}` +
		`if(typeof u.href==='string'){var r=rfw(u.href);return r!==u.href?r:u;}` +
		`}` +
		`return u;` +
		`}` +
		`if(u.startsWith(p+'/')||u===p)return u;` +
		`for(var i=0;i<gh.length;i++){if(u.startsWith(gh[i]))return p+'/'+u;}` +
		`if(u.startsWith('/'))return p+u;` +
		`return u;` +
		`}` +
		`var of=window.fetch;window.fetch=function(u,o){u=rfw(u);return of.call(this,u,o);};` +
		`var oo=XMLHttpRequest.prototype.open;XMLHttpRequest.prototype.open=function(){var a=Array.prototype.slice.call(arguments);if(a.length>1){a[1]=rfw(a[1]);}return oo.apply(this,a);};` +
		`if(typeof EventSource!=='undefined'){var oes=window.EventSource;window.EventSource=function(u,o){u=rfw(u);return new oes(u,o);};}` +
		`if(typeof WebSocket!=='undefined'){var ows=window.WebSocket;window.WebSocket=function(u,p){if(typeof u==='string'){u=rfw(u);}return new ows(u,p);};}` +
		`function fix(root){` +
		`root.querySelectorAll&&root.querySelectorAll('a[href]').forEach(function(a){a.href=rw(a.getAttribute('href'))});` +
		`root.querySelectorAll&&root.querySelectorAll('form[action]').forEach(function(f){f.action=rw(f.getAttribute('action'))});` +
		`root.querySelectorAll&&root.querySelectorAll('[src]').forEach(function(el){var s=el.getAttribute('src');if(s){var rs=rw(s);if(rs!==s)el.setAttribute('src',rs)}});` +
		`}` +
		`fix(document);` +
		`if(window.MutationObserver){` +
		`new MutationObserver(function(ms){ms.forEach(function(m){m.addedNodes.forEach(function(n){if(n.nodeType===1)fix(n)})})}).observe(document.documentElement||document.body,{childList:true,subtree:true});` +
		`}` +
		`document.addEventListener('click',function(e){` +
		`var a=e.target.closest('a');` +
		`if(!a)return;` +
		`var h=a.getAttribute('href');` +
		`var rh=rw(h);` +
		`if(rh!==h){` +
		`e.preventDefault();` +
		`location.href=rh;` +
		`}` +
		`},true);` +
		`document.addEventListener('submit',function(e){` +
		`var f=e.target.closest('form');` +
		`if(!f)return;` +
		`var h=f.getAttribute('action');` +
		`var rh=rw(h);` +
		`if(rh!==h){` +
		`f.setAttribute('action',rh);` +
		`}` +
		`},true);` +
		`})();` +
		`</script>`)

	// 优先在 <head> 标签后插入，确保比页面其他脚本先执行
	if idx := bytes.Index(body, []byte("<head>")); idx != -1 {
		pos := idx + len("<head>")
		return append(body[:pos], append(script, body[pos:]...)...)
	}
	if idx := bytes.Index(body, []byte("<head ")); idx != -1 {
		endIdx := bytes.Index(body[idx:], []byte(">"))
		if endIdx != -1 {
			pos := idx + endIdx + 1
			return append(body[:pos], append(script, body[pos:]...)...)
		}
	}
	// 回退到 </head> 前
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
