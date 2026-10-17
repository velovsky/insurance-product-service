// Package store keeps products and their audit outbox. The in-memory
// implementation is the reference for a database-backed one: every mutation
// and its event commit together, under one lock here and one transaction there.
package store

import (
	"sort"
	"sync"
	"time"

	"github.com/velovsky/insurance-product-service/internal/domain"
)

// Mutation changes a product in place and reports what happened. Returning an
// error discards the change and writes no event.
type Mutation func(p *domain.Product) (eventType string, versionNumber int, err error)

type Memory struct {
	mu       sync.RWMutex
	products map[string]*domain.Product
	events   []domain.Event
}

func NewMemory() *Memory {
	return &Memory{products: map[string]*domain.Product{}}
}

func (m *Memory) Create(p *domain.Product, actor string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.products[p.Code]; ok {
		return &domain.Error{Kind: domain.KindAlreadyExists, Message: "product " + p.Code + " already exists"}
	}
	m.products[p.Code] = p.Clone()
	m.appendEvent(domain.EventProductCreated, p.Code, 0, actor, now)
	return nil
}

func (m *Memory) Get(code string) (*domain.Product, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.products[code]
	if !ok {
		return nil, notFound(code)
	}
	return p.Clone(), nil
}

// Update applies the mutation to a copy and swaps it in only on success, so a
// failed mutation can never leave a half-changed product behind.
func (m *Memory) Update(code, actor string, now time.Time, mutate Mutation) (*domain.Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.products[code]
	if !ok {
		return nil, notFound(code)
	}
	next := current.Clone()
	eventType, version, err := mutate(next)
	if err != nil {
		return nil, err
	}
	m.products[code] = next
	m.appendEvent(eventType, code, version, actor, now)
	return next.Clone(), nil
}

func (m *Memory) Events(afterSequence int64, limit int) []domain.Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	start := sort.Search(len(m.events), func(i int) bool { return m.events[i].Sequence > afterSequence })
	end := min(start+limit, len(m.events))
	return append([]domain.Event(nil), m.events[start:end]...)
}

func (m *Memory) appendEvent(eventType, code string, version int, actor string, now time.Time) {
	m.events = append(m.events, domain.Event{
		Sequence:      int64(len(m.events) + 1),
		Type:          eventType,
		ProductCode:   code,
		VersionNumber: version,
		Actor:         actor,
		OccurredAt:    now,
	})
}

func notFound(code string) error {
	return &domain.Error{Kind: domain.KindNotFound, Message: "product " + code + " not found"}
}
