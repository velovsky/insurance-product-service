package domain

import (
	"errors"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

func date(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func kindOf(err error) Kind {
	var de *Error
	if errors.As(err, &de) {
		return de.Kind
	}
	return 0
}

// publish walks a draft through review: alice proposes, bob approves.
func publish(t *testing.T, p *Product, tariff Tariff, effective string) Version {
	t.Helper()
	v, err := p.AddDraft(tariff, date(t, effective), "test", "alice", now)
	if err != nil {
		t.Fatalf("AddDraft: %v", err)
	}
	if _, err := p.Approve(v.Number, "bob", now); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	pub, err := p.Publish(v.Number, now)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return pub
}

func newTravelProduct(t *testing.T) *Product {
	t.Helper()
	p, err := NewProduct("travel-basic", "Travel Basic", "travel")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFourEyes(t *testing.T) {
	p := newTravelProduct(t)
	v, _ := p.AddDraft(travelTariff(), date(t, "2026-11-01"), "launch", "agent:pricing-bot", now)

	if _, err := p.Approve(v.Number, "agent:pricing-bot", now); kindOf(err) != KindForbidden {
		t.Fatalf("author approving own draft: want KindForbidden, got %v", err)
	}
	approved, err := p.Approve(v.Number, "bob", now)
	if err != nil {
		t.Fatalf("Approve by reviewer: %v", err)
	}
	if approved.Status != StatusApproved || approved.ApprovedBy != "bob" {
		t.Errorf("got %+v", approved)
	}
}

func TestOnlyOnePendingChange(t *testing.T) {
	p := newTravelProduct(t)
	if _, err := p.AddDraft(travelTariff(), date(t, "2026-11-01"), "", "alice", now); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AddDraft(travelTariff(), date(t, "2026-12-01"), "", "alice", now); kindOf(err) != KindConflict {
		t.Fatalf("second pending draft: want KindConflict, got %v", err)
	}
}

func TestInvalidDraftCannotBeApproved(t *testing.T) {
	p := newTravelProduct(t)
	bad := travelTariff()
	bad.Factors[0].MultipliersBP["65+"] = 150000 // 15x: a typo for 1.5x
	v, err := p.AddDraft(bad, date(t, "2026-11-01"), "", "alice", now)
	if err != nil {
		t.Fatalf("invalid drafts must still be stored for review: %v", err)
	}
	if _, err := p.Approve(v.Number, "bob", now); kindOf(err) != KindInvalid {
		t.Fatalf("want KindInvalid, got %v", err)
	}
}

func TestEffectiveDateRules(t *testing.T) {
	p := newTravelProduct(t)
	publish(t, p, travelTariff(), "2026-11-01")

	tests := []struct {
		name      string
		effective string
		wantIssue bool
	}{
		{"in the past", "2026-10-01", true},
		{"same day as the published version", "2026-11-01", true},
		{"before the published version", "2026-10-20", true},
		{"after the published version", "2027-01-01", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := p.Clone()
			v, err := c.AddDraft(travelTariff(), date(t, tt.effective), "", "alice", now)
			if err != nil {
				t.Fatal(err)
			}
			issues, _ := c.ValidateVersion(v.Number, now)
			if got := len(issues) > 0; got != tt.wantIssue {
				t.Errorf("issues = %+v, want issue: %v", issues, tt.wantIssue)
			}
		})
	}
}

func TestPublishRechecksTimeline(t *testing.T) {
	p := newTravelProduct(t)
	v, _ := p.AddDraft(travelTariff(), date(t, "2026-10-18"), "", "alice", now)
	if _, err := p.Approve(v.Number, "bob", now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(48 * time.Hour) // the effective date has passed meanwhile
	if _, err := p.Publish(v.Number, later); kindOf(err) != KindConflict {
		t.Fatalf("want KindConflict, got %v", err)
	}
}

func TestInForce(t *testing.T) {
	p := newTravelProduct(t)
	publish(t, p, travelTariff(), "2026-11-01")
	repriced := travelTariff()
	repriced.BasePremium.Minor = 11000
	publish(t, p, repriced, "2027-01-01")

	tests := []struct {
		asOf        string
		wantVersion int // 0 = nothing in force
	}{
		{"2026-10-31", 0},
		{"2026-11-01", 1},
		{"2026-12-31", 1},
		{"2027-01-01", 2},
		{"2030-06-15", 2},
	}
	for _, tt := range tests {
		v, ok := p.InForce(date(t, tt.asOf))
		got := 0
		if ok {
			got = v.Number
		}
		if got != tt.wantVersion {
			t.Errorf("InForce(%s) = v%d, want v%d", tt.asOf, got, tt.wantVersion)
		}
	}
}

func TestDryRunShowsDiffAndImpact(t *testing.T) {
	p := newTravelProduct(t)
	publish(t, p, travelTariff(), "2026-11-01")

	proposal := travelTariff()
	proposal.Factors[1].MultipliersBP["world"] = 15000
	v, _ := p.AddDraft(proposal, date(t, "2027-01-01"), "world cover costs more", "agent:pricing-bot", now)

	res, err := p.DryRun(v.Number, []map[string]string{
		{"age_band": "18-30", "destination": "world"},
		{"age_band": "31-64", "destination": "europe"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid() || res.ComparedToVersion != 1 {
		t.Fatalf("got %+v", res)
	}
	if len(res.Changes) != 1 || res.Changes[0].Path != "factors.destination.world" {
		t.Errorf("changes = %+v", res.Changes)
	}
	world, europe := res.Impacts[0], res.Impacts[1]
	if world.Current.Minor != 19992 || world.Proposed.Minor != 21420 || world.ChangeBP != 714 {
		t.Errorf("world impact = %+v (current %v)", world, *world.Current)
	}
	if europe.ChangeBP != 0 {
		t.Errorf("europe should not change: %+v", europe)
	}

	if len(p.Versions) != 2 || p.Versions[1].Status != StatusDraft {
		t.Errorf("dry run must not change state: %+v", p.Versions)
	}
}
