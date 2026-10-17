package domain

import (
	"fmt"
	"strings"
)

// Kind classifies domain errors so transports can map them to their own codes.
type Kind int

const (
	KindInvalid Kind = iota + 1
	KindNotFound
	KindAlreadyExists
	KindConflict
	KindForbidden
)

// Issue is one validation problem, addressed by a dotted field path.
type Issue struct {
	Field   string
	Message string
}

type Error struct {
	Kind    Kind
	Message string
	Issues  []Issue
}

func (e *Error) Error() string {
	if len(e.Issues) == 0 {
		return e.Message
	}
	parts := make([]string, len(e.Issues))
	for i, is := range e.Issues {
		parts[i] = is.Field + ": " + is.Message
	}
	return e.Message + " (" + strings.Join(parts, "; ") + ")"
}

func errorf(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

func invalid(message string, issues []Issue) *Error {
	return &Error{Kind: KindInvalid, Message: message, Issues: issues}
}
