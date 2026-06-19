// Package export copies invoices into the warehouse.
package export

import (
	"context"
	"errors"
	"time"

	"example.com/ledger-export/internal/tallyline"
)

// Sink stores exported rows.
type Sink interface {
	Write(rows []tallyline.Invoice) error
}

type Job struct {
	Client *tallyline.Client
	Sink   Sink
	// Backoff is the wait time after a rate limit answer.
	Backoff time.Duration
	Sleep   func(time.Duration)
}

func NewJob(client *tallyline.Client, sink Sink) *Job {
	return &Job{Client: client, Sink: sink, Backoff: 2 * time.Minute, Sleep: time.Sleep}
}

// Run exports all invoices of one account.
func (j *Job) Run(ctx context.Context, account string) error {
	cursor := ""
	for {
		resp, err := j.Client.ListInvoices(ctx, tallyline.ListRequest{Account: account, Cursor: cursor})
		if errors.Is(err, tallyline.ErrRateLimited) {
			j.Sleep(j.Backoff)
			continue
		}
		if err != nil {
			return err
		}
		if err := j.Sink.Write(resp.Invoices); err != nil {
			return err
		}
		if resp.NextCursor == "" {
			return nil
		}
		cursor = resp.NextCursor
	}
}
