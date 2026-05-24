package proxy

import (
	"bytes"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// rewriteHTMLBody parses HTML and rewrites absolute URLs in known attributes
// so they include the token prefix.  This is needed because many sites (e.g.
// GitHub) use Content-Security-Policy that blocks inline scripts, making the
// JS-injection approach unreliable.
func rewriteHTMLBody(body []byte, prefix string) []byte {
	if prefix == "" {
		return body
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return body
	}

	rewriteNode(doc, prefix)

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

func rewriteNode(n *html.Node, prefix string) {
	if n.Type == html.ElementNode {
		for i := range n.Attr {
			attr := &n.Attr[i]
			if isURLAttr(attr.Key) {
				attr.Val = rewriteURL(attr.Val, prefix)
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
		rewriteNode(c, prefix)
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

var refreshURLRe = regexp.MustCompile(`(?i)(url\s*=\s*)(["']?)(/[^"'>\s;]*)\2`)

func rewriteRefreshContent(content, prefix string) string {
	return refreshURLRe.ReplaceAllStringFunc(content, func(m string) string {
		matches := refreshURLRe.FindStringSubmatch(m)
		if len(matches) < 5 {
			return m
		}
		urlPart := matches[3]
		rewritten := rewriteURL(urlPart, prefix)
		if rewritten == urlPart {
			return m
		}
		return matches[1] + matches[2] + rewritten + matches[2]
	})
}

// cssURLRe matches url(/path), url("/path"), url('/path').
var cssURLRe = regexp.MustCompile(`(?i)(url\s*\(\s*)(["']?)(/[^"')\s]*)\2(\s*\))`)

func rewriteCSSURLs(css, prefix string) string {
	return cssURLRe.ReplaceAllStringFunc(css, func(m string) string {
		matches := cssURLRe.FindStringSubmatch(m)
		if len(matches) < 5 {
			return m
		}
		urlPart := matches[3]
		rewritten := rewriteURL(urlPart, prefix)
		if rewritten == urlPart {
			return m
		}
		return matches[1] + matches[2] + rewritten + matches[2] + matches[4]
	})
}
