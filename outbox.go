package catalog

import (
	"context"
	"time"
)

const OutboxChannel = "catalog_outbox"

// OutboxReader is a trusted relay port. Confirm only after durable consumption.
// Pending reads do not claim rows; the relay serializes each (source, subject).
type OutboxReader interface {
	PendingOutbox(context.Context, int) ([]Envelope, error)
	ConfirmOutbox(context.Context, string) error
}

type Envelope struct {
	SpecVersion     string     `json:"specversion"`
	ID              string     `json:"id"`
	Source          string     `json:"source"`
	Type            string     `json:"type"`
	Subject         string     `json:"subject"`
	Time            time.Time  `json:"time"`
	SchoolID        string     `json:"schoolid,omitempty"`
	DataContentType string     `json:"datacontenttype,omitempty"`
	Data            ResultData `json:"data"`
}

// Optional pointers preserve discovery's empty title, false visibility and zero
// ratings while ordinary business events carry only their revision.
type ResultData struct {
	Revision int64            `json:"revision"`
	Target   *DiscoveryTarget `json:"target,omitempty"`
	Title    *string          `json:"title,omitempty"`
	SchoolID *string          `json:"school_id,omitempty"`
	Visible  *bool            `json:"visible,omitempty"`
	Ratings  *[5]int          `json:"ratings,omitempty"`
	At       *time.Time       `json:"at,omitempty"`
}
