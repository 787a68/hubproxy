package handlers

import "net/http"

// hopByHopHeaders HTTP/1.1 逐跳头部
// 这些头部在代理重定向时不应转发，避免 Content-Length/Encoding 不匹配
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailers":            true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func isHopByHopHeader(key string) bool {
	return hopByHopHeaders[http.CanonicalHeaderKey(key)]
}
