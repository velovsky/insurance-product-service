package domain

import (
	"maps"
	"regexp"
	"strconv"
	"time"
)

type Status int

const (
	StatusDraft Status = iota + 1
	StatusApproved
	StatusPublished
)

func (s Status) String() string {
	switch s {
	case StatusDraft:
		return "draft"
	case StatusApproved:
		return "approved"
	case StatusPublished:
		return "published"
	default:
		return "unknown"
	}
}

// Version is one tariff of a product. Once published it never changes; a new
// price is a new version with a later effective date, so a policy can always
// be re-priced with the rules it was sold under.
type Version struct {
	Number        int
	Status        Status
	EffectiveFrom time.Time
	Tariff        Tariff
	ChangeNote    string
	CreatedBy     string
	ApprovedBy    string
	CreatedAt     time.Time
	PublishedAt   time.Time
}

type Product struct {
	Code     string
	Name     string
	Line     string
	Versions []Version
}

var codePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

func NewProduct(code, name, line string) (*Product, error) {
	var issues []Issue
	if !codePattern.MatchString(code) {
		issues = append(issues, Issue{Field: "code", Message: "must be 2-63 lowercase letters, digits or dashes"})
	}
	if name == "" {
		issues = append(issues, Issue{Field: "name", Message: "must not be empty"})
	}
	if line == "" {
		issues = append(issues, Issue{Field: "line", Message: "must not be empty"})
	}
	if len(issues) > 0 {
		return nil, invalid("invalid product", issues)
	}
	return &Product{Code: code, Name: name, Line: line}, nil
}

// AddDraft stores a proposal even when it is not valid yet: an agent's
// proposal should be inspectable and fixable, not silently dropped. Validity
// is enforced at approval. Only one change can be pending at a time.
func (p *Product) AddDraft(t Tariff, effectiveFrom time.Time, note, actor string, now time.Time) (Version, error) {
	for _, v := range p.Versions {
		if v.Status != StatusPublished {
			return Version{}, errorf(KindConflict, "version %d is still %s; publish or replace it first", v.Number, v.Status)
		}
	}
	v := Version{
		Number:        len(p.Versions) + 1,
		Status:        StatusDraft,
		EffectiveFrom: effectiveFrom,
		Tariff:        cloneTariff(t),
		ChangeNote:    note,
		CreatedBy:     actor,
		CreatedAt:     now,
	}
	p.Versions = append(p.Versions, v)
	return v, nil
}

// ValidateVersion checks the tariff and the timeline rules for a pending version.
func (p *Product) ValidateVersion(number int, now time.Time) ([]Issue, error) {
	v, err := p.version(number)
	if err != nil {
		return nil, err
	}
	issues := ValidateTariff(v.Tariff)
	if v.EffectiveFrom.Before(Today(now)) {
		issues = append(issues, Issue{Field: "effective_from", Message: "must not be in the past"})
	}
	if latest, ok := p.latestPublished(); ok && !v.EffectiveFrom.After(latest.EffectiveFrom) {
		issues = append(issues, Issue{
			Field:   "effective_from",
			Message: "must be after " + FormatDate(latest.EffectiveFrom) + ", the start of published version " + strconv.Itoa(latest.Number),
		})
	}
	return issues, nil
}

// Approve enforces the four-eyes principle: whoever proposed a tariff, person
// or agent, cannot also approve it.
func (p *Product) Approve(number int, actor string, now time.Time) (Version, error) {
	v, err := p.version(number)
	if err != nil {
		return Version{}, err
	}
	if v.Status != StatusDraft {
		return Version{}, errorf(KindConflict, "version %d is %s, only drafts can be approved", number, v.Status)
	}
	if actor == v.CreatedBy {
		return Version{}, errorf(KindForbidden, "%s created version %d and cannot approve it", actor, number)
	}
	issues, _ := p.ValidateVersion(number, now)
	if len(issues) > 0 {
		return Version{}, invalid("version cannot be approved", issues)
	}
	v.Status = StatusApproved
	v.ApprovedBy = actor
	return *v, nil
}

// Publish re-checks the timeline because time has passed since approval.
func (p *Product) Publish(number int, now time.Time) (Version, error) {
	v, err := p.version(number)
	if err != nil {
		return Version{}, err
	}
	if v.Status != StatusApproved {
		return Version{}, errorf(KindConflict, "version %d is %s, only approved versions can be published", number, v.Status)
	}
	if issues, _ := p.ValidateVersion(number, now); len(issues) > 0 {
		return Version{}, &Error{Kind: KindConflict, Message: "version can no longer be published", Issues: issues}
	}
	v.Status = StatusPublished
	v.PublishedAt = now
	return *v, nil
}

// InForce returns the published version that applies on the given date.
func (p *Product) InForce(asOf time.Time) (Version, bool) {
	var found *Version
	for i := range p.Versions {
		v := &p.Versions[i]
		if v.Status != StatusPublished || v.EffectiveFrom.After(asOf) {
			continue
		}
		if found == nil || v.EffectiveFrom.After(found.EffectiveFrom) {
			found = v
		}
	}
	if found == nil {
		return Version{}, false
	}
	return *found, true
}

func (p *Product) Version(number int) (Version, error) {
	v, err := p.version(number)
	if err != nil {
		return Version{}, err
	}
	return *v, nil
}

func (p *Product) version(number int) (*Version, error) {
	if number < 1 || number > len(p.Versions) {
		return nil, errorf(KindNotFound, "product %s has no version %d", p.Code, number)
	}
	return &p.Versions[number-1], nil
}

func (p *Product) latestPublished() (Version, bool) {
	var latest *Version
	for i := range p.Versions {
		v := &p.Versions[i]
		if v.Status == StatusPublished && (latest == nil || v.EffectiveFrom.After(latest.EffectiveFrom)) {
			latest = v
		}
	}
	if latest == nil {
		return Version{}, false
	}
	return *latest, true
}

// Clone returns a deep copy, so callers outside the store cannot mutate state.
func (p *Product) Clone() *Product {
	c := *p
	c.Versions = make([]Version, len(p.Versions))
	for i, v := range p.Versions {
		v.Tariff = cloneTariff(v.Tariff)
		c.Versions[i] = v
	}
	return &c
}

func cloneTariff(t Tariff) Tariff {
	c := t
	c.Factors = make([]RatingFactor, len(t.Factors))
	for i, f := range t.Factors {
		c.Factors[i] = RatingFactor{Name: f.Name, MultipliersBP: maps.Clone(f.MultipliersBP)}
	}
	return c
}
