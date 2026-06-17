package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/austiecodes/rolio/internal/store"
)

func NewHandler(adapter store.Adapter) http.Handler { return &handler{adapter: adapter} }

type handler struct{ adapter store.Adapter }

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		fmt.Fprintln(w, "ok")
		return
	}
	op := strings.TrimPrefix(r.URL.Path, "/v1/")
	reads := map[string]bool{"ls": true, "tree": true, "cat": true, "grep": true, "find": true, "stat": true, "search": true, "glob": true}
	method := http.MethodGet
	if op == "write" || op == "edit" {
		method = http.MethodPut
	} else if op == "delete" {
		method = http.MethodDelete
	} else if !reads[op] {
		http.NotFound(w, r)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeJSONErrorCode(w, 405, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		http.NotFound(w, r)
		return
	}
	if strings.Contains(r.URL.Query().Get("path"), "://") {
		writeJSONError(w, store.ErrInvalidParam)
		return
	}
	var resp any
	var err error
	switch method {
	case http.MethodGet:
		resp, err = h.dispatchRead(r, h.adapter, op)
	case http.MethodPut:
		resp, err = h.dispatchPut(r, h.adapter, op)
	case http.MethodDelete:
		resp, err = h.dispatchDelete(r, h.adapter, op)
	}
	if err != nil {
		writeJSONError(w, err)
		return
	}
	if cat, ok := resp.(*store.CatResponse); ok && cat.Hash != "" {
		w.Header().Set("ETag", `"`+cat.Hash+`"`)
		if etagMatch(r.Header.Get("If-None-Match"), cat.Hash) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	writeJSON(w, resp)
}

func (h *handler) dispatchRead(r *http.Request, adapter store.Adapter, op string) (any, error) {
	q := r.URL.Query()
	switch op {
	case "ls":
		limit, err := queryIntNonNeg(q, "limit")
		if err != nil {
			return nil, err
		}
		offset, err := queryIntNonNeg(q, "offset")
		if err != nil {
			return nil, err
		}
		return adapter.LS(r.Context(), store.LSRequest{
			Path:      queryPath(q),
			Sort:      q.Get("sort"),
			Reverse:   queryBoolOr(q, "reverse"),
			Recursive: queryBoolOr(q, "recursive"),
			All:       queryBoolOr(q, "all"),
			Limit:     limit,
			Offset:    offset,
		})
	case "tree":
		depth, err := queryInt(q, "depth")
		if err != nil {
			return nil, err
		}
		return adapter.Tree(r.Context(), store.TreeRequest{
			Path:      queryPath(q),
			Depth:     depth,
			All:       queryBoolOr(q, "all"),
			DirsOnly:  queryBoolOr(q, "dirs_only"),
			FullPath:  queryBoolOr(q, "full_path"),
			ShowSize:  queryBoolOr(q, "show_size"),
			Sort:      q.Get("sort"),
			DirsFirst: queryBoolOr(q, "dirs_first"),
		})
	case "cat":
		return adapter.Cat(r.Context(), store.CatRequest{Path: queryPath(q)})
	case "grep":
		regex, err := queryBool(q, "regex")
		if err != nil {
			return nil, err
		}
		ctxBefore, _ := queryInt(q, "context_before")
		ctxAfter, _ := queryInt(q, "context_after")
		return adapter.Grep(r.Context(), store.GrepRequest{
			Path:            queryPath(q),
			Pattern:         q.Get("pattern"),
			Regex:           regex,
			CaseInsensitive: queryBoolOr(q, "case_insensitive"),
			Invert:          queryBoolOr(q, "invert"),
			WholeWord:       queryBoolOr(q, "whole_word"),
			WholeLine:       queryBoolOr(q, "whole_line"),
			ContextBefore:   ctxBefore,
			ContextAfter:    ctxAfter,
			All:             queryBoolOr(q, "all"),
			Include:         q.Get("include"),
			Exclude:         q.Get("exclude"),
		})
	case "find":
		maxDepth, _ := queryInt(q, "maxdepth")
		minDepth, _ := queryInt(q, "mindepth")
		limit, err := queryIntNonNeg(q, "limit")
		if err != nil {
			return nil, err
		}
		offset, err := queryIntNonNeg(q, "offset")
		if err != nil {
			return nil, err
		}
		return adapter.Find(r.Context(), store.FindRequest{
			Path:     queryPath(q),
			Name:     q.Get("name"),
			Type:     q.Get("type"),
			MaxDepth: maxDepth,
			MinDepth: minDepth,
			All:      queryBoolOr(q, "all"),
			IName:    q.Get("iname"),
			Limit:    limit,
			Offset:   offset,
		})
	case "stat":
		return adapter.Stat(r.Context(), store.StatRequest{Path: queryPath(q)})
	case "search":
		limit, err := queryIntNonNeg(q, "limit")
		if err != nil {
			return nil, err
		}
		offset, err := queryIntNonNeg(q, "offset")
		if err != nil {
			return nil, err
		}
		return adapter.Search(r.Context(), store.SearchRequest{
			Query:  q.Get("q"),
			Path:   queryPath(q),
			Limit:  limit,
			Offset: offset,
		})
	case "glob":
		globber, ok := adapter.(store.Globber)
		if !ok {
			return nil, fmt.Errorf("glob is not supported by this backend")
		}
		pattern := q.Get("pattern")
		if pattern == "" {
			return nil, fmt.Errorf("%w: pattern is required", store.ErrInvalidParam)
		}
		globLimit, err := queryIntNonNeg(q, "limit")
		if err != nil {
			return nil, err
		}
		globOffset, err := queryIntNonNeg(q, "offset")
		if err != nil {
			return nil, err
		}
		return globber.Glob(r.Context(), store.GlobRequest{
			Pattern: pattern,
			Limit:   globLimit,
			Offset:  globOffset,
		})
	default:
		return nil, fmt.Errorf("unknown operation: %s", op)
	}
}

func (h *handler) dispatchPut(r *http.Request, adapter store.Adapter, op string) (any, error) {
	q := r.URL.Query()
	switch op {
	case "write":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
		return adapter.Put(r.Context(), store.PutRequest{
			Path:         queryPath(q),
			Content:      string(body),
			ExpectedHash: expectedHash(r, q),
		})
	case "edit":
		var editReq struct {
			Old string `json:"old"`
			New string `json:"new"`
			All bool   `json:"all"`
		}
		if err := json.NewDecoder(r.Body).Decode(&editReq); err != nil {
			return nil, fmt.Errorf("decode edit body: %w", err)
		}
		return adapter.Edit(r.Context(), store.EditRequest{
			Path:         queryPath(q),
			Old:          editReq.Old,
			New:          editReq.New,
			All:          editReq.All,
			ExpectedHash: expectedHash(r, q),
		})
	default:
		return nil, fmt.Errorf("unknown operation: %s", op)
	}
}

func (h *handler) dispatchDelete(r *http.Request, adapter store.Adapter, op string) (any, error) {
	q := r.URL.Query()
	switch op {
	case "delete":
		return adapter.Delete(r.Context(), store.DeleteRequest{
			Path:         queryPath(q),
			ExpectedHash: expectedHash(r, q),
		})
	default:
		return nil, fmt.Errorf("unknown operation: %s", op)
	}
}

func queryPath(q url.Values) string {
	if p := q.Get("path"); p != "" {
		return p
	}
	return "/"
}

// queryIntNonNeg parses a non-negative integer query parameter.
func queryIntNonNeg(q url.Values, key string) (int, error) {
	raw := q.Get(key)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid %s", store.ErrInvalidParam, key)
	}
	if value < 0 {
		return 0, fmt.Errorf("%w: %s must be non-negative", store.ErrInvalidParam, key)
	}
	return value, nil
}

func queryInt(q url.Values, key string) (int, error) {
	raw := q.Get(key)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid %s: %v", store.ErrInvalidParam, key, err)
	}
	return value, nil
}

func queryBool(q url.Values, key string) (bool, error) {
	raw := q.Get(key)
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%w: invalid %s: %v", store.ErrInvalidParam, key, err)
	}
	return value, nil
}

func queryBoolOr(q url.Values, key string) bool {
	v, _ := queryBool(q, key)
	return v
}

func writeJSON(w http.ResponseWriter, resp any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeJSONStatus(w http.ResponseWriter, status int, resp any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeJSONError(w http.ResponseWriter, err error) {
	status, code := mapError(err)
	writeJSONErrorCode(w, status, code, err.Error())
}

// etagMatch checks whether the If-None-Match header value matches the given
// content hash. Supports both quoted ("sha256:...") and unquoted forms,
// as well as comma-separated multiple ETags per RFC 7232.
func etagMatch(ifNoneMatch, hash string) bool {
	quoted := `"` + hash + `"`
	for _, tag := range strings.Split(ifNoneMatch, ",") {
		t := strings.TrimSpace(tag)
		if t == hash || t == quoted {
			return true
		}
	}
	return false
}

func writeJSONErrorCode(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

// expectedHash returns the CAS hash from If-Match header or expected_hash query param.
// If-None-Match: * maps to "*" (create-only).
func expectedHash(r *http.Request, q url.Values) string {
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" {
		return strings.Trim(ifMatch, `"`)
	}
	if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch == "*" {
		return "*"
	}
	return q.Get("expected_hash")
}
func mapError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrOldNotFound):
		return http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, store.ErrIsDir), errors.Is(err, store.ErrNotDir),
		errors.Is(err, store.ErrEmptyOld), errors.Is(err, store.ErrCannotDeleteRoot),
		errors.Is(err, store.ErrEmptyQuery),
		errors.Is(err, store.ErrInvalidParam):
		return http.StatusBadRequest, "BAD_REQUEST"
	case errors.Is(err, store.ErrContentNotReady):
		return http.StatusNotFound, "CONTENT_NOT_READY"
	case errors.Is(err, store.ErrNotSupported):
		return http.StatusNotImplemented, "NOT_SUPPORTED"
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict, "CONFLICT"
	default:
		return http.StatusInternalServerError, "INTERNAL_ERROR"
	}
}
