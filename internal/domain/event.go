package domain

import "time"

const (
	EventProductCreated   = "product.created"
	EventDraftCreated     = "version.draft_created"
	EventVersionApproved  = "version.approved"
	EventVersionPublished = "version.published"
)

// Event is an audit record. It is written in the same transaction as the
// state change it describes (outbox pattern), so the log can never miss or
// invent a change.
type Event struct {
	Sequence      int64
	Type          string
	ProductCode   string
	VersionNumber int
	Actor         string
	OccurredAt    time.Time
}
