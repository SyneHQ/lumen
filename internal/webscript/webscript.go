// Package webscript serves Lumen's drop-in browser analytics script.
package webscript

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"time"
)

//go:embed script.js
var script []byte

var etag = func() string { s := sha256.Sum256(script); return `"` + hex.EncodeToString(s[:]) + `"` }()

// Handler serves script.js with caching, ETag and CORS headers.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/javascript; charset=utf-8")
		h.Set("Cache-Control", "public, max-age=3600")
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("ETag", etag)
		// ServeContent handles If-None-Match -> 304, HEAD and ranges.
		http.ServeContent(w, r, "script.js", time.Time{}, bytes.NewReader(script))
	})
}
