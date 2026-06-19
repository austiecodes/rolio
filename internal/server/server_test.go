package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemovedRoutesAndWrongMethods(t *testing.T) {
	h := NewHandler(nil)
	for _, p := range []string{"/v1/repos", "/v1/docsets", "/v1/docs/cat?name=x", "/v1/mount-sources", "/v1/hashes", "/v1/locate", "/v1/cache", "/v1/usage-events"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/write", nil))
	if w.Code != 405 || w.Header().Get("Allow") != "PUT" {
		t.Fatalf("wrong method: %d %v", w.Code, w.Header())
	}
}

func TestUIAndContextBoundary(t *testing.T) {
	h := NewHandler(nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Rolio") || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("UI: %d %s", w.Code, w.Body.String())
	}
	for _, op := range []string{"refresh", "reindex"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/"+op, nil))
		if w.Code != 405 {
			t.Errorf("GET %s: %d", op, w.Code)
		}
		r := httptest.NewRequest("POST", "/v1/"+op, nil)
		r.Header.Set("Origin", "https://other.example")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Errorf("cross-origin %s: %d", op, w.Code)
		}
	}
	for _, p := range []string{"/v1/missing", "/not-a-ui-route"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}
