package main

import (
	"time"

	"github.com/velovsky/insurance-product-service/internal/domain"
	"github.com/velovsky/insurance-product-service/internal/store"
)

const demoProductCode = "travel-basic"

// seedDemo publishes version 1 of a travel product through the normal
// lifecycle, so the demo data obeys the same rules as real data.
func seedDemo(repo *store.Memory, now time.Time) error {
	p, err := domain.NewProduct(demoProductCode, "Travel Basic", "travel")
	if err != nil {
		return err
	}
	if err := repo.Create(p, "seed:author", now); err != nil {
		return err
	}

	tariff := domain.Tariff{
		BasePremium: domain.Money{Currency: "EUR", Minor: 4900},
		Factors: []domain.RatingFactor{
			{Name: "age_band", MultipliersBP: map[string]int64{"18-30": 11000, "31-64": 10000, "65+": 16000}},
			{Name: "destination", MultipliersBP: map[string]int64{"europe": 10000, "world": 14500}},
			{Name: "trip_length", MultipliersBP: map[string]int64{"week": 10000, "month": 18000}},
		},
		MinimumPremium: domain.Money{Currency: "EUR", Minor: 2500},
		TaxRateBP:      1900,
	}
	today := domain.Today(now)
	steps := []struct {
		actor string
		event string
		apply func(p *domain.Product) (domain.Version, error)
	}{
		{"seed:author", domain.EventDraftCreated, func(p *domain.Product) (domain.Version, error) {
			return p.AddDraft(tariff, today, "Initial launch tariff", "seed:author", now)
		}},
		{"seed:reviewer", domain.EventVersionApproved, func(p *domain.Product) (domain.Version, error) {
			return p.Approve(1, "seed:reviewer", now)
		}},
		{"seed:reviewer", domain.EventVersionPublished, func(p *domain.Product) (domain.Version, error) {
			return p.Publish(1, now)
		}},
	}
	for _, s := range steps {
		if _, err := repo.Update(demoProductCode, s.actor, now, func(p *domain.Product) (string, int, error) {
			v, err := s.apply(p)
			return s.event, v.Number, err
		}); err != nil {
			return err
		}
	}
	return nil
}
