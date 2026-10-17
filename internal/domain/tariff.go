package domain

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
)

// RatingFactor scales the premium by the multiplier of the option a risk selects.
type RatingFactor struct {
	Name          string
	MultipliersBP map[string]int64
}

type Tariff struct {
	BasePremium    Money
	Factors        []RatingFactor
	MinimumPremium Money
	TaxRateBP      int64
}

// Guardrails for a single multiplier. They stop a typo (or an over-eager
// agent) from turning a 1.2x surcharge into 12x before anyone reviews it.
const (
	minMultiplierBP = 1000  // 0.1x
	maxMultiplierBP = 50000 // 5.0x
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// ValidateTariff returns every problem at once, so a reviewer (human or AI)
// can fix a proposal in one pass.
func ValidateTariff(t Tariff) []Issue {
	var issues []Issue
	add := func(field, format string, args ...any) {
		issues = append(issues, Issue{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if !currencyPattern.MatchString(t.BasePremium.Currency) {
		add("tariff.base_premium.currency", "must be a 3-letter ISO 4217 code")
	}
	if t.BasePremium.Minor <= 0 {
		add("tariff.base_premium.amount_minor", "must be greater than zero")
	}
	if t.MinimumPremium.Minor < 0 {
		add("tariff.minimum_premium.amount_minor", "must not be negative")
	}
	if t.MinimumPremium.Minor > 0 && t.MinimumPremium.Currency != t.BasePremium.Currency {
		add("tariff.minimum_premium.currency", "must match the base premium currency %q", t.BasePremium.Currency)
	}
	if t.TaxRateBP < 0 || t.TaxRateBP > basisPoints {
		add("tariff.tax_rate_bp", "must be between 0 and %d", basisPoints)
	}

	seen := map[string]bool{}
	for i, f := range t.Factors {
		field := fmt.Sprintf("tariff.factors[%d]", i)
		switch {
		case f.Name == "":
			add(field+".name", "must not be empty")
		case seen[f.Name]:
			add(field+".name", "duplicate factor %q", f.Name)
		}
		seen[f.Name] = true
		if len(f.MultipliersBP) == 0 {
			add(field+".multipliers_bp", "must define at least one option")
		}
		for _, option := range sortedKeys(f.MultipliersBP) {
			bp := f.MultipliersBP[option]
			if option == "" {
				add(field+".multipliers_bp", "option name must not be empty")
			}
			if bp < minMultiplierBP || bp > maxMultiplierBP {
				add(field+".multipliers_bp."+option, "multiplier %s is outside the allowed range %s-%s",
					formatBP(bp), formatBP(minMultiplierBP), formatBP(maxMultiplierBP))
			}
		}
	}
	return issues
}

type PremiumLine struct {
	Label  string
	Amount Money
}

type Quote struct {
	Lines []PremiumLine
	Net   Money
	Tax   Money
	Total Money
}

// Price applies the base premium, each rating factor in tariff order, the
// minimum premium and finally the premium tax. Every step is a line, so the
// result explains itself.
func Price(t Tariff, risk map[string]string) (Quote, error) {
	var issues []Issue
	known := map[string]bool{}
	for _, f := range t.Factors {
		known[f.Name] = true
		option, ok := risk[f.Name]
		if !ok {
			issues = append(issues, Issue{Field: "risk.attributes." + f.Name, Message: "is required"})
			continue
		}
		if _, ok := f.MultipliersBP[option]; !ok {
			issues = append(issues, Issue{
				Field:   "risk.attributes." + f.Name,
				Message: fmt.Sprintf("unknown option %q, expected one of %v", option, sortedKeys(f.MultipliersBP)),
			})
		}
	}
	for _, name := range sortedKeys(risk) {
		if !known[name] {
			issues = append(issues, Issue{Field: "risk.attributes." + name, Message: "is not a rating factor of this tariff"})
		}
	}
	if len(issues) > 0 {
		return Quote{}, invalid("risk cannot be priced", issues)
	}

	cur := t.BasePremium.Currency
	money := func(minor int64) Money { return Money{Currency: cur, Minor: minor} }

	net := t.BasePremium.Minor
	lines := []PremiumLine{{Label: "Base premium", Amount: money(net)}}
	for _, f := range t.Factors {
		option := risk[f.Name]
		bp := f.MultipliersBP[option]
		adjusted := applyBP(net, bp)
		lines = append(lines, PremiumLine{
			Label:  fmt.Sprintf("%s = %s (x%s)", f.Name, option, formatBP(bp)),
			Amount: money(adjusted - net),
		})
		net = adjusted
	}
	if net < t.MinimumPremium.Minor {
		lines = append(lines, PremiumLine{Label: "Minimum premium adjustment", Amount: money(t.MinimumPremium.Minor - net)})
		net = t.MinimumPremium.Minor
	}
	tax := applyBP(net, t.TaxRateBP)
	lines = append(lines, PremiumLine{Label: "Insurance premium tax (" + formatBP(t.TaxRateBP*100) + "%)", Amount: money(tax)})

	return Quote{Lines: lines, Net: money(net), Tax: money(tax), Total: money(net + tax)}, nil
}

// Change is one difference between two tariffs. Before or After is empty when
// a value was added or removed.
type Change struct {
	Path   string
	Before string
	After  string
}

// DiffTariffs lists what a reviewer must look at, in a stable order.
func DiffTariffs(before, after Tariff) []Change {
	var changes []Change
	add := func(path, b, a string) {
		if b != a {
			changes = append(changes, Change{Path: path, Before: b, After: a})
		}
	}
	add("base_premium", before.BasePremium.String(), after.BasePremium.String())
	add("minimum_premium", before.MinimumPremium.String(), after.MinimumPremium.String())
	add("tax_rate_bp", strconv.FormatInt(before.TaxRateBP, 10), strconv.FormatInt(after.TaxRateBP, 10))

	beforeFactors := factorsByName(before.Factors)
	afterFactors := factorsByName(after.Factors)
	names := sortedKeys(beforeFactors)
	for _, n := range sortedKeys(afterFactors) {
		if _, ok := beforeFactors[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		b, inBefore := beforeFactors[name]
		a, inAfter := afterFactors[name]
		switch {
		case !inBefore:
			add("factors."+name, "", "added")
		case !inAfter:
			add("factors."+name, "present", "")
		}
		options := sortedKeys(b.MultipliersBP)
		for _, o := range sortedKeys(a.MultipliersBP) {
			if !slices.Contains(options, o) {
				options = append(options, o)
			}
		}
		sort.Strings(options)
		for _, o := range options {
			add("factors."+name+"."+o, bpOrEmpty(b.MultipliersBP, o), bpOrEmpty(a.MultipliersBP, o))
		}
	}
	return changes
}

func factorsByName(fs []RatingFactor) map[string]RatingFactor {
	m := make(map[string]RatingFactor, len(fs))
	for _, f := range fs {
		m[f.Name] = f
	}
	return m
}

func bpOrEmpty(m map[string]int64, key string) string {
	bp, ok := m[key]
	if !ok {
		return ""
	}
	return "x" + formatBP(bp)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
