package server

import (
	"net/http"
	"net/http/httptest"
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
