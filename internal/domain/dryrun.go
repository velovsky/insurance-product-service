package domain

import (
	"fmt"
	"time"
)

type SampleImpact struct {
	Risk     map[string]string
	Current  *Money // nil when no version is in force to compare with
	Proposed Money
	ChangeBP int64
}

// DryRunResult is everything a reviewer needs to approve a pending version
// without reading the raw tariff: what is wrong, what changes and what it
// does to real prices.
type DryRunResult struct {
	Issues            []Issue
	ComparedToVersion int
	Changes           []Change
	Impacts           []SampleImpact
}

func (r DryRunResult) Valid() bool { return len(r.Issues) == 0 }

// DryRun evaluates a pending version against the version it would replace on
// its effective date. It never changes state.
func (p *Product) DryRun(number int, samples []map[string]string, now time.Time) (DryRunResult, error) {
	v, err := p.version(number)
	if err != nil {
		return DryRunResult{}, err
	}
	if v.Status == StatusPublished {
		return DryRunResult{}, errorf(KindConflict, "version %d is already published", number)
	}

	var res DryRunResult
	res.Issues, _ = p.ValidateVersion(number, now)

	current, hasCurrent := p.InForce(v.EffectiveFrom)
	if hasCurrent {
		res.ComparedToVersion = current.Number
		res.Changes = DiffTariffs(current.Tariff, v.Tariff)
	} else {
		res.Changes = DiffTariffs(Tariff{}, v.Tariff)
	}

	for i, risk := range samples {
		proposed, err := Price(v.Tariff, risk)
		if err != nil {
			res.Issues = append(res.Issues, Issue{Field: fmt.Sprintf("sample_risks[%d]", i), Message: err.Error()})
			continue
		}
		impact := SampleImpact{Risk: risk, Proposed: proposed.Total}
		if hasCurrent {
			// A sample that the current tariff cannot price (e.g. a new
			// factor) still shows its proposed price, just without a delta.
			if cur, err := Price(current.Tariff, onlyKnownAttributes(current.Tariff, risk)); err == nil {
				impact.Current = &cur.Total
				if cur.Total.Minor > 0 {
					impact.ChangeBP = (proposed.Total.Minor - cur.Total.Minor) * basisPoints / cur.Total.Minor
				}
			}
		}
		res.Impacts = append(res.Impacts, impact)
	}
	return res, nil
}

// onlyKnownAttributes drops attributes the tariff does not rate, so a sample
// written for a new factor can still be priced under the old tariff.
func onlyKnownAttributes(t Tariff, risk map[string]string) map[string]string {
	out := map[string]string{}
	for _, f := range t.Factors {
		if v, ok := risk[f.Name]; ok {
			out[f.Name] = v
		}
	}
	return out
}
