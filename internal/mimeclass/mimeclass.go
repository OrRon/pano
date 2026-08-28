// Package mimeclass buckets Content-Type values into the short classes pano
// shows in list rows and uses in filters and views.
package mimeclass

import (
	"mime"
	"strings"
)

// Of buckets a Content-Type into: json, sse, html, js, css, img, font, form,
// xml, text, media, bin, or "" when unknown.
func Of(ct string) string {
	if ct == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	}
	switch {
	case mt == "application/json" || strings.HasSuffix(mt, "+json") || mt == "text/json":
		return "json"
	case mt == "text/event-stream":
		return "sse"
	case mt == "text/html" || mt == "application/xhtml+xml":
		return "html"
	case mt == "application/javascript" || mt == "text/javascript" || mt == "application/x-javascript":
		return "js"
	case mt == "text/css":
		return "css"
	case strings.HasPrefix(mt, "image/"):
		return "img"
	case strings.HasPrefix(mt, "font/") || strings.Contains(mt, "font"):
		return "font"
	case mt == "application/x-www-form-urlencoded" || strings.HasPrefix(mt, "multipart/"):
		return "form"
	case strings.HasSuffix(mt, "xml"):
		return "xml"
	case strings.HasPrefix(mt, "text/") || mt == "application/x-ndjson" || mt == "application/jsonl":
		return "text"
	case strings.HasPrefix(mt, "video/") || strings.HasPrefix(mt, "audio/"):
		return "media"
	default:
		return "bin"
	}
}

// IsTextual reports whether a class can be shown inline as text.
func IsTextual(class string) bool {
	switch class {
	case "json", "sse", "html", "js", "css", "form", "xml", "text", "":
		return true
	}
	return false
}

// MediaType returns the bare media type of a Content-Type.
func MediaType(ct string) string {
	if ct == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	}
	return mt
}
