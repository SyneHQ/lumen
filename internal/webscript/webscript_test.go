package webscript

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/js/script.js", nil))
	if rec.Code != 200 || rec.Body.Len() != len(script) {
		t.Fatalf("code=%d len=%d", rec.Code, rec.Body.Len())
	}
	want := map[string]string{
		"Content-Type":                "application/javascript; charset=utf-8",
		"Cache-Control":               "public, max-age=3600",
		"Access-Control-Allow-Origin": "*",
		"ETag":                        etag,
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if len(etag) != 66 {
		t.Errorf("etag %q not a quoted sha256", etag)
	}

	req := httptest.NewRequest("GET", "/js/script.js", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("304 expected, got %d len=%d", rec.Code, rec.Body.Len())
	}
}
