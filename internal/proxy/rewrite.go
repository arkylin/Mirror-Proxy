package proxy

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

// rewriteHTMLBody parses HTML and rewrites absolute URLs in known attributes
// so they include the token prefix.  This is needed because many sites (e.g.
// GitHub) use Content-Security-Policy that blocks inline scripts, making the
// JS-injection approach unreliable.
func rewriteHTMLBody(body []byte, prefix string, host string, proto string) []byte {
	if prefix == "" {
		return body
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return body
	}

	rewriteNode(doc, prefix, host, proto)

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return body
	}
	return buf.Bytes()
}

// urlAttrs lists element attributes that contain URLs which should be rewritten.
var urlAttrs = []string{
	"href",
	"src",
	"action",
	"poster",
	"formaction",
	"cite",
	"longdesc",
	"profile",
	"background",
	"data-url",
	"data-href",
}

func rewriteNode(n *html.Node, prefix string, host string, proto string) {
	if n.Type == html.ElementNode {
		for i := range n.Attr {
			attr := &n.Attr[i]
			if isURLAttr(attr.Key) {
				attr.Val = rewriteURL(attr.Val, prefix)
				// 将 href 的相对路径显式写成绝对路径，避免浏览器根据当前
				// 页面协议猜测（防止 http 页面中的链接被解析成 https）。
				if host != "" && proto != "" &&
					strings.EqualFold(attr.Key, "href") &&
					strings.HasPrefix(attr.Val, "/") &&
					!strings.HasPrefix(attr.Val, "//") {
					attr.Val = proto + "://" + host + attr.Val
				}
				continue
			}
			// <meta http-equiv="refresh" content="0;url=/path">
			if strings.EqualFold(n.Data, "meta") &&
				strings.EqualFold(attr.Key, "content") &&
				isRefreshMeta(n) {
				attr.Val = rewriteRefreshContent(attr.Val, prefix)
			}
		}

		// Rewrite url(...) inside <style> tags.
		if strings.EqualFold(n.Data, "style") && n.FirstChild != nil &&
			n.FirstChild.Type == html.TextNode {
			n.FirstChild.Data = rewriteCSSURLs(n.FirstChild.Data, prefix)
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		rewriteNode(c, prefix, host, proto)
	}
}

func isURLAttr(key string) bool {
	lower := strings.ToLower(key)
	for _, attr := range urlAttrs {
		if lower == attr {
			return true
		}
	}
	return false
}

func isRefreshMeta(n *html.Node) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "http-equiv") && strings.EqualFold(a.Val, "refresh") {
			return true
		}
	}
	return false
}

// rewriteRefreshContent rewrites URLs inside meta refresh content,
// e.g. "0;url=/path" -> "0;url={prefix}/path"
func rewriteRefreshContent(content, prefix string) string {
	for _, pat := range []string{"url=\"", "url='", "url="} {
		idx := strings.Index(content, pat)
		if idx == -1 {
			continue
		}
		start := idx + len(pat)
		quote := ""
		if pat == "url=\"" {
			quote = "\""
		} else if pat == "url='" {
			quote = "'"
		}
		end := len(content)
		if quote != "" {
			if q := strings.Index(content[start:], quote); q != -1 {
				end = start + q
			}
		} else {
			for i := start; i < len(content); i++ {
				if content[i] == ' ' || content[i] == ';' || content[i] == '"' || content[i] == '\'' {
					end = i
					break
				}
			}
		}
		urlPart := content[start:end]
		rewritten := rewriteURL(urlPart, prefix)
		if rewritten != urlPart {
			return content[:start] + rewritten + content[end:]
		}
	}
	return content
}

// rewriteCSSURLs rewrites url(/path) inside CSS text.
func rewriteCSSURLs(css, prefix string) string {
	var result strings.Builder
	i := 0
	for i < len(css) {
		idx := strings.Index(css[i:], "url(")
		if idx == -1 {
			result.WriteString(css[i:])
			break
		}
		idx += i
		result.WriteString(css[i:idx])

		start := idx + 4 // after "url("
		// skip whitespace
		for start < len(css) && (css[start] == ' ' || css[start] == '\t' || css[start] == '\n') {
			start++
		}
		if start >= len(css) {
			result.WriteString(css[idx:])
			break
		}

		quote := ""
		if css[start] == '"' || css[start] == '\'' {
			quote = string(css[start])
			start++
		}

		urlStart := start
		urlEnd := len(css)
		if quote != "" {
			if q := strings.Index(css[start:], quote); q != -1 {
				urlEnd = start + q
			}
		} else {
			for j := start; j < len(css); j++ {
				if css[j] == ')' || css[j] == ' ' || css[j] == '\t' || css[j] == '\n' {
					urlEnd = j
					break
				}
			}
		}

		urlPart := css[urlStart:urlEnd]
		rewritten := rewriteURL(urlPart, prefix)

		result.WriteString("url(")
		if quote != "" {
			result.WriteString(quote)
		}
		result.WriteString(rewritten)
		if quote != "" {
			result.WriteString(quote)
		}

		// find closing ')'
		i = urlEnd
		if quote != "" {
			i++ // skip closing quote
		}
		for i < len(css) && (css[i] == ' ' || css[i] == '\t' || css[i] == '\n') {
			i++
		}
		if i < len(css) && css[i] == ')' {
			i++
		}
	}
	return result.String()
}
