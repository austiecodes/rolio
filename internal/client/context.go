package client

import (
	"context"
	"github.com/austiecodes/rolio/internal/store"
	"net/http"
	"net/url"
)

func (c *Client) Summary(ctx context.Context, p string) (*store.Summary, error) {
	var result store.Summary
	err := c.get(ctx, "summary", url.Values{"path": {p}}, &result)
	return &result, err
}
func (c *Client) Refresh(ctx context.Context, p string) (*store.Summary, error) {
	var result store.Summary
	err := c.contextPost(ctx, "refresh", url.Values{"path": {p}}, &result)
	return &result, err
}
func (c *Client) Reindex(ctx context.Context) (*store.IndexStatus, error) {
	var result store.IndexStatus
	err := c.contextPost(ctx, "reindex", nil, &result)
	return &result, err
}
func (c *Client) Settings(ctx context.Context) (*store.Settings, error) {
	var result store.Settings
	err := c.get(ctx, "settings", nil, &result)
	return &result, err
}
func (c *Client) contextPost(ctx context.Context, op string, q url.Values, out any) error {
	endpoint, err := c.url(op, q)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	return c.doWithAllowed(req, op, out, nil)
}
