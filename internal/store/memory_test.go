package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/velovsky/insurance-product-service/internal/domain"
)

var now = time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

func TestFailedMutationLeavesNoTrace(t *testing.T) {
	m := NewMemory()
	p, _ := domain.NewProduct("travel-basic", "Travel Basic", "travel")
	if err := m.Create(p, "alice", now); err != nil {
		t.Fatal(err)
	}

	_, err := m.Update("travel-basic", "alice", now, func(p *domain.Product) (string, int, error) {
		p.Name = "half-changed"
		return "", 0, errors.New("boom")
	})
	if err == nil {
		t.Fatal("want error")
	}
	got, _ := m.Get("travel-basic")
	if got.Name != "Travel Basic" {
		t.Errorf("failed mutation leaked: name = %q", got.Name)
	}
	if evs := m.Events(0, 10); len(evs) != 1 {
		t.Errorf("failed mutation wrote an event: %+v", evs)
	}
}

func TestGetReturnsACopy(t *testing.T) {
	m := NewMemory()
	p, _ := domain.NewProduct("travel-basic", "Travel Basic", "travel")
	_ = m.Create(p, "alice", now)
	got, _ := m.Get("travel-basic")
	got.Name = "mutated by caller"
	again, _ := m.Get("travel-basic")
	if again.Name != "Travel Basic" {
		t.Errorf("caller mutated stored state")
	}
}

func TestEventsAreOrderedAndPaged(t *testing.T) {
	m := NewMemory()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, _ := domain.NewProduct("p-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "P", "travel")
			_ = m.Create(p, "alice", now)
		}()
	}
	wg.Wait()

	first := m.Events(0, 20)
	rest := m.Events(first[len(first)-1].Sequence, 100)
	if len(first) != 20 || len(rest) != 30 {
		t.Fatalf("paging: got %d + %d events", len(first), len(rest))
	}
	all := append(first, rest...)
	for i, e := range all {
		if e.Sequence != int64(i+1) {
			t.Fatalf("event %d has sequence %d", i, e.Sequence)
		}
	}
}
