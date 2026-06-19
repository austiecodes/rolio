// Package tallyline is the client for the Tallyline billing service.
package tallyline

import (
	"context"
	"errors"
)

// ErrRateLimited is returned when the service answers HTTP 429.
var ErrRateLimited = errors.New("tallyline: rate limited")

// ErrSnapshotExpired is returned when the service answers HTTP 410.
var ErrSnapshotExpired = errors.New("tallyline: snapshot expired")

type Invoice struct {
	ID     string
	Amount int64
}

type ListRequest struct {
	Account string
	// Cursor continues a listing. A cursor is valid for 90 seconds only.
	// The service does not reject an expired cursor: it ignores the cursor
	// and returns the first page again.
	Cursor string
	// SnapshotID pins the listing. Copy it from the first response into each
	// subsequent request. With a snapshot, cursors do not expire silently:
	// the service answers 410 (ErrSnapshotExpired) after 15 minutes.
	SnapshotID string
}

type ListResponse struct {
	Invoices   []Invoice
	NextCursor string
	SnapshotID string
}

// Transport sends one list request.
type Transport interface {
	List(ctx context.Context, req ListRequest) (ListResponse, error)
}

type Client struct{ Transport Transport }

func (c *Client) ListInvoices(ctx context.Context, req ListRequest) (ListResponse, error) {
	return c.Transport.List(ctx, req)
}
