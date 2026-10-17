package domain

import (
	"errors"
	"testing"
)

func travelTariff() Tariff {
	return Tariff{
		BasePremium: Money{Currency: "EUR", Minor: 10000},
		Factors: []RatingFactor{
			{Name: "age_band", MultipliersBP: map[string]int64{"18-30": 12000, "31-64": 10000, "65+": 15000}},
			{Name: "destination", MultipliersBP: map[string]int64{"europe": 10000, "world": 14000}},
		},
		MinimumPremium: Money{Currency: "EUR", Minor: 5000},
		TaxRateBP:      1900,
	}
}

func TestPrice(t *testing.T) {
	cheap := Tariff{
		BasePremium:    Money{Currency: "EUR", Minor: 10000},
		Factors:        []RatingFactor{{Name: "discount", MultipliersBP: map[string]int64{"loyal": 2000}}},
		MinimumPremium: Money{Currency: "EUR", Minor: 5000},
		TaxRateBP:      1900,
	}
	rounding := Tariff{
		BasePremium: Money{Currency: "EUR", Minor: 333},
		Factors:     []RatingFactor{{Name: "risk", MultipliersBP: map[string]int64{"high": 15000}}},
	}

	tests := []struct {
		name      string
		tariff    Tariff
		risk      map[string]string
		wantNet   int64
		wantTax   int64
		wantTotal int64
		wantLines int
	}{
		{"factors multiply in order", travelTariff(), map[string]string{"age_band": "18-30", "destination": "world"}, 16800, 3192, 19992, 4},
		{"neutral factors keep the base", travelTariff(), map[string]string{"age_band": "31-64", "destination": "europe"}, 10000, 1900, 11900, 4},
		{"minimum premium lifts a low net", cheap, map[string]string{"discount": "loyal"}, 5000, 950, 5950, 4},
		{"half a cent rounds up", rounding, map[string]string{"risk": "high"}, 500, 0, 500, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := Price(tt.tariff, tt.risk)
			if err != nil {
				t.Fatalf("Price: %v", err)
			}
			if q.Net.Minor != tt.wantNet || q.Tax.Minor != tt.wantTax || q.Total.Minor != tt.wantTotal {
				t.Errorf("net/tax/total = %d/%d/%d, want %d/%d/%d", q.Net.Minor, q.Tax.Minor, q.Total.Minor, tt.wantNet, tt.wantTax, tt.wantTotal)
			}
			if len(q.Lines) != tt.wantLines {
				t.Errorf("got %d lines, want %d: %+v", len(q.Lines), tt.wantLines, q.Lines)
			}
			var sum int64
			for _, l := range q.Lines {
				sum += l.Amount.Minor
			}
			if sum != q.Total.Minor {
				t.Errorf("lines sum to %d, total is %d", sum, q.Total.Minor)
			}
		})
	}
}

func TestPriceRejectsBadRisk(t *testing.T) {
	_, err := Price(travelTariff(), map[string]string{"age_band": "12-17", "smoker": "yes"})
	var de *Error
	if !errors.As(err, &de) || de.Kind != KindInvalid {
		t.Fatalf("want KindInvalid, got %v", err)
	}
	want := map[string]bool{
		"risk.attributes.age_band":    true, // unknown option
		"risk.attributes.destination": true, // missing
		"risk.attributes.smoker":      true, // not a factor
	}
	if len(de.Issues) != len(want) {
		t.Fatalf("got issues %+v", de.Issues)
	}
	for _, is := range de.Issues {
		if !want[is.Field] {
			t.Errorf("unexpected issue %+v", is)
		}
	}
}

func TestValidateTariff(t *testing.T) {
	if issues := ValidateTariff(travelTariff()); len(issues) != 0 {
		t.Fatalf("valid tariff reported issues: %+v", issues)
	}

	bad := travelTariff()
	bad.BasePremium.Currency = "euro"
	bad.TaxRateBP = 12000
	bad.Factors = append(bad.Factors,
		RatingFactor{Name: "age_band", MultipliersBP: map[string]int64{"x": 10000}},
		RatingFactor{Name: "typo", MultipliersBP: map[string]int64{"oops": 120000}},
	)
	issues := ValidateTariff(bad)
	fields := map[string]bool{}
	for _, is := range issues {
		fields[is.Field] = true
	}
	for _, f := range []string{
		"tariff.base_premium.currency",
		"tariff.tax_rate_bp",
		"tariff.factors[2].name",
		"tariff.factors[3].multipliers_bp.oops",
	} {
		if !fields[f] {
			t.Errorf("missing issue for %s in %+v", f, issues)
		}
	}
}

func TestDiffTariffs(t *testing.T) {
	after := travelTariff()
	after.Factors[1].MultipliersBP["world"] = 15000
	after.Factors = append(after.Factors, RatingFactor{Name: "trip_length", MultipliersBP: map[string]int64{"short": 10000}})

	got := DiffTariffs(travelTariff(), after)
	want := []Change{
		{Path: "factors.destination.world", Before: "x1.4", After: "x1.5"},
		{Path: "factors.trip_length", Before: "", After: "added"},
		{Path: "factors.trip_length.short", Before: "", After: "x1"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("change %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
