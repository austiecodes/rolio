package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/austiecodes/rolio/internal/store"
)

type Client struct {
	baseURL string
	http    *http.Client
}

var _ store.Adapter = (*Client)(nil)

func New(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: http.DefaultClient}
}

func (c *Client) LS(ctx context.Context, req store.LSRequest) (*store.LSResponse, error) {
	var resp store.LSResponse
	q := url.Values{"path": {req.Path}}
	if req.Sort != "" {
		q.Set("sort", req.Sort)
	}
	if req.Reverse {
		q.Set("reverse", "true")
	}
	if req.Recursive {
		q.Set("recursive", "true")
	}
	if req.All {
		q.Set("all", "true")
	}
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Offset > 0 {
		q.Set("offset", strconv.Itoa(req.Offset))
	}
	if err := c.get(ctx, "ls", q, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Tree(ctx context.Context, req store.TreeRequest) (*store.TreeResponse, error) {
	var resp store.TreeResponse
	q := url.Values{"path": {req.Path}}
	if req.Depth > 0 {
		q.Set("depth", strconv.Itoa(req.Depth))
	}
	if req.All {
		q.Set("all", "true")
	}
	if req.DirsOnly {
		q.Set("dirs_only", "true")
	}
	if req.FullPath {
		q.Set("full_path", "true")
	}
	if req.ShowSize {
		q.Set("show_size", "true")
	}
	if req.Sort != "" {
		q.Set("sort", req.Sort)
	}
	if req.DirsFirst {
		q.Set("dirs_first", "true")
	}
	if err := c.get(ctx, "tree", q, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Cat(ctx context.Context, req store.CatRequest) (*store.CatResponse, error) {
	q := url.Values{"path": {req.Path}}
	endpoint, err := c.url("cat", q)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build cat request: %w", err)
	}

	// Send If-None-Match header if caller provides a known hash.
	if req.IfNoneMatch != "" {
		httpReq.Header.Set("If-None-Match", `"`+req.IfNoneMatch+`"`)
	}

	// Use doWithAllowed so 304 is handled gracefully while preserving
	// the shared JSON error parsing for non-2xx responses.
	var catResp store.CatResponse
	err = c.doWithAllowed(httpReq, "cat", &catResp, []int{http.StatusNotModified})
	if err != nil {
		return nil, err
	}

	// doWithAllowed returns nil for allowed codes (304) without decoding.
	// Path is always populated on 200, zero-valued on undecoded 304.
	if catResp.Path == "" && req.IfNoneMatch != "" {
		return &store.CatResponse{Path: req.Path, Hash: req.IfNoneMatch}, store.ErrNotModified
	}
	return &catResp, nil
}

func (c *Client) Grep(ctx context.Context, req store.GrepRequest) (*store.GrepResponse, error) {
	var resp store.GrepResponse
	q := url.Values{
		"path":    {req.Path},
		"pattern": {req.Pattern},
	}
	if req.Regex {
		q.Set("regex", "true")
	}
	if req.CaseInsensitive {
		q.Set("case_insensitive", "true")
	}
	if req.Invert {
		q.Set("invert", "true")
	}
	if req.WholeWord {
		q.Set("whole_word", "true")
	}
	if req.WholeLine {
		q.Set("whole_line", "true")
	}
	if req.ContextBefore > 0 {
		q.Set("context_before", strconv.Itoa(req.ContextBefore))
	}
	if req.ContextAfter > 0 {
		q.Set("context_after", strconv.Itoa(req.ContextAfter))
	}
	if req.All {
		q.Set("all", "true")
	}
	if req.Include != "" {
		q.Set("include", req.Include)
	}
	if req.Exclude != "" {
		q.Set("exclude", req.Exclude)
	}
	if err := c.get(ctx, "grep", q, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Find(ctx context.Context, req store.FindRequest) (*store.FindResponse, error) {
	var resp store.FindResponse
	q := url.Values{"path": {req.Path}}
	if req.Name != "" {
		q.Set("name", req.Name)
	}
	if req.Type != "" {
		q.Set("type", req.Type)
	}
	if req.MaxDepth > 0 {
		q.Set("maxdepth", strconv.Itoa(req.MaxDepth))
	}
	if req.MinDepth > 0 {
		q.Set("mindepth", strconv.Itoa(req.MinDepth))
	}
	if req.All {
		q.Set("all", "true")
	}
	if req.IName != "" {
		q.Set("iname", req.IName)
	}
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Offset > 0 {
		q.Set("offset", strconv.Itoa(req.Offset))
	}
	if err := c.get(ctx, "find", q, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Stat(ctx context.Context, req store.StatRequest) (*store.StatResponse, error) {
	var resp store.StatResponse
	q := url.Values{"path": {req.Path}}
	if err := c.get(ctx, "stat", q, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Put(ctx context.Context, req store.PutRequest) (*store.PutResponse, error) {
	var resp store.PutResponse
	q := url.Values{"path": {req.Path}}
	if req.ExpectedHash != "" {
		q.Set("expected_hash", req.ExpectedHash)
	}
	if err := c.putWithHeaders(ctx, "write", q, req.Content, &resp, req.ExpectedHash); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Delete(ctx context.Context, req store.DeleteRequest) (*store.DeleteResponse, error) {
	q := url.Values{"path": {req.Path}}
	if req.ExpectedHash != "" {
		q.Set("expected_hash", req.ExpectedHash)
	}
	if err := c.deleteWithHeaders(ctx, q); err != nil {
		return nil, err
	}
	return &store.DeleteResponse{}, nil
}

func (c *Client) Edit(ctx context.Context, req store.EditRequest) (*store.EditResponse, error) {
	var resp store.EditResponse
	q := url.Values{"path": {req.Path}}
	if req.ExpectedHash != "" {
		q.Set("expected_hash", req.ExpectedHash)
	}
	body, _ := json.Marshal(map[string]any{"old": req.Old, "new": req.New, "all": req.All})
	if err := c.putJSONWithHeaders(ctx, "edit", q, body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Search(ctx context.Context, req store.SearchRequest) (*store.SearchResponse, error) {
	var resp store.SearchResponse
	q := url.Values{"q": {req.Query}}
	if req.Path != "" {
		q.Set("path", req.Path)
	}
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Offset > 0 {
		q.Set("offset", strconv.Itoa(req.Offset))
	}
	if err := c.get(ctx, "search", q, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Glob(ctx context.Context, req store.GlobRequest) (*store.GlobResponse, error) {
	var resp store.GlobResponse
	q := url.Values{"pattern": {req.Pattern}}
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Offset > 0 {
		q.Set("offset", strconv.Itoa(req.Offset))
	}
	if err := c.get(ctx, "glob", q, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) get(ctx context.Context, op string, q url.Values, out any) error {
	endpoint, err := c.url(op, q)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build %s request: %w", op, err)
	}
	return c.do(req, op, out)
}

// putWithHeaders is like put but sets conditional write headers.
func (c *Client) putWithHeaders(ctx context.Context, op string, q url.Values, body string, out any, expectedHash string) error {
	endpoint, err := c.url(op, q)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("build %s request: %w", op, err)
	}
	req.Header.Set("Content-Type", "text/plain")
	c.setWriteHeaders(req, expectedHash)
	return c.do(req, op, out)
}

// putJSONWithHeaders is like putJSON but sets conditional write headers.
func (c *Client) putJSONWithHeaders(ctx context.Context, op string, q url.Values, body []byte, out any) error {
	endpoint, err := c.url(op, q)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build %s request: %w", op, err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setWriteHeaders(req, q.Get("expected_hash"))
	return c.do(req, op, out)
}

// deleteWithHeaders is like delete but sets conditional write headers.
func (c *Client) deleteWithHeaders(ctx context.Context, q url.Values) error {
	endpoint, err := c.url("delete", q)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}
	c.setWriteHeaders(req, q.Get("expected_hash"))
	return c.do(req, "delete", nil)
}

// setWriteHeaders adds conditional write headers.
func (c *Client) setWriteHeaders(req *http.Request, expectedHash string) {
	if expectedHash == "*" {
		req.Header.Set("If-None-Match", "*")
	} else if expectedHash != "" {
		req.Header.Set("If-Match", `"`+expectedHash+`"`)
	}
}

func (c *Client) do(req *http.Request, op string, out any) error {
	return c.doWithAllowed(req, op, out, nil)
}

// doWithAllowed is like do but accepts additional allowed status codes
// beyond the normal 2xx range. For allowed codes, no JSON decode is attempted
// and the caller handles the response.
func (c *Client) doWithAllowed(req *http.Request, op string, out any, allowed []int) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call %s: %w", op, err)
	}
	defer resp.Body.Close()

	// Check if this is an explicitly allowed non-2xx status (e.g. 304).
	for _, code := range allowed {
		if resp.StatusCode == code {
			return nil
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		var errResp struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
			err := fmt.Errorf("%s failed: status %d: %s", op, resp.StatusCode, errResp.Error.Message)
			switch errResp.Error.Code {
			case "NOT_FOUND":
				return fmt.Errorf("%w: %w", store.ErrNotFound, err)
			case "CONFLICT":
				return fmt.Errorf("%w: %w", store.ErrConflict, err)
			case "NOT_SUPPORTED":
				return fmt.Errorf("%w: %w", store.ErrNotSupported, err)
			case "CONTENT_NOT_READY":
				return fmt.Errorf("%w: %w", store.ErrContentNotReady, err)
			}
			return err
		}
		return fmt.Errorf("%s failed: status %d: %s", op, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode %s response: %w", op, err)
		}
	}
	return nil
}

func (c *Client) url(op string, q url.Values) (string, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v1/" + op
	base.RawPath = ""
	base.RawQuery = q.Encode()
	return base.String(), nil
}
