package summary

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatGenerator(t *testing.T) {
	for _, language := range []string{"zh", "en"} {
		t.Run(language, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string                           `json:"model"`
					Messages []struct{ Role, Content string } `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Model != "test-model" || len(body.Messages) != 2 || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("request: %+v", body)
				}
				want := "English"
				if language == "zh" {
					want = "Simplified Chinese"
				}
				if !strings.Contains(body.Messages[0].Content, want) {
					t.Error("missing language instruction")
				}
				fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"{\"abstract\":\"简短摘要\",\"overview\":\"# Overview\"}"}}]}`)
			}))
			defer srv.Close()
			g := ChatGenerator{URL: srv.URL, Model: "test-model", APIKey: "test-key"}
			r, err := g.Generate(context.Background(), Request{Language: language, Path: "/docs", Directory: true, Sources: []Source{{Path: "/docs/a.md", Body: "text"}}})
			if err != nil || r.Abstract != "简短摘要" {
				t.Fatal(r, err)
			}
		})
	}
}
func TestProviderFailureDoesNotLeakResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, "secret-provider-details")
	}))
	defer srv.Close()
	g := ChatGenerator{URL: srv.URL, Model: "test"}
	_, err := g.Generate(context.Background(), Request{Language: "en", Path: "/", Directory: true})
	if err == nil || strings.Contains(err.Error(), "secret-provider-details") {
		t.Fatal(err)
	}
}

// A summary above the limits is cut, not refused.
func TestLongSummaryIsCut(t *testing.T) {
	long := strings.Repeat("长", 300)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, _ := json.Marshal(Result{Abstract: long, Overview: "ok"})
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]string{"content": string(content)}}}})
	}))
	defer srv.Close()
	g := ChatGenerator{URL: srv.URL, Model: "test"}
	r, err := g.Generate(context.Background(), Request{Language: "zh", Path: "/a.md", Sources: []Source{{Path: "/a.md", Body: "text"}}})
	if err != nil || len([]rune(r.Abstract)) != 256 || r.Overview != "ok" {
		t.Fatal(len([]rune(r.Abstract)), r.Overview, err)
	}
}
