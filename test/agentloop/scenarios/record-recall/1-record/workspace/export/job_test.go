package export

import (
	"context"
	"testing"
	"time"

	"example.com/ledger-export/internal/tallyline"
)

type pages struct{ calls int }

func (p *pages) List(_ context.Context, req tallyline.ListRequest) (tallyline.ListResponse, error) {
	p.calls++
	if req.Cursor == "" {
		return tallyline.ListResponse{Invoices: []tallyline.Invoice{{ID: "a"}}, NextCursor: "c1", SnapshotID: "s1"}, nil
	}
	return tallyline.ListResponse{Invoices: []tallyline.Invoice{{ID: "b"}}}, nil
}

type rows struct{ ids []string }

func (r *rows) Write(in []tallyline.Invoice) error {
	for _, i := range in {
		r.ids = append(r.ids, i.ID)
	}
	return nil
}

func TestRunExportsAllPages(t *testing.T) {
	sink := &rows{}
	job := NewJob(&tallyline.Client{Transport: &pages{}}, sink)
	job.Sleep = func(time.Duration) {}
	if err := job.Run(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	if len(sink.ids) != 2 {
		t.Fatalf("got %v", sink.ids)
	}
}
