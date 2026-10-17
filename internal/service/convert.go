package service

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	productv1 "github.com/velovsky/insurance-product-service/gen/insurance/product/v1"
	"github.com/velovsky/insurance-product-service/internal/domain"
)

func toProto(p *domain.Product) *productv1.Product {
	out := &productv1.Product{Code: p.Code, Name: p.Name, Line: p.Line}
	for _, v := range p.Versions {
		out.Versions = append(out.Versions, versionToProto(v))
	}
	return out
}

func versionToProto(v domain.Version) *productv1.ProductVersion {
	pv := &productv1.ProductVersion{
		Number:        int32(v.Number),
		Status:        statusToProto(v.Status),
		EffectiveFrom: domain.FormatDate(v.EffectiveFrom),
		Tariff:        tariffToProto(v.Tariff),
		ChangeNote:    v.ChangeNote,
		CreatedBy:     v.CreatedBy,
		ApprovedBy:    v.ApprovedBy,
		CreatedAt:     timestamppb.New(v.CreatedAt),
	}
	if !v.PublishedAt.IsZero() {
		pv.PublishedAt = timestamppb.New(v.PublishedAt)
	}
	return pv
}

func statusToProto(s domain.Status) productv1.VersionStatus {
	switch s {
	case domain.StatusDraft:
		return productv1.VersionStatus_VERSION_STATUS_DRAFT
	case domain.StatusApproved:
		return productv1.VersionStatus_VERSION_STATUS_APPROVED
	case domain.StatusPublished:
		return productv1.VersionStatus_VERSION_STATUS_PUBLISHED
	default:
		return productv1.VersionStatus_VERSION_STATUS_UNSPECIFIED
	}
}

func moneyToProto(m domain.Money) *productv1.Money {
	return &productv1.Money{Currency: m.Currency, AmountMinor: m.Minor}
}

func moneyFromProto(m *productv1.Money) domain.Money {
	return domain.Money{Currency: m.GetCurrency(), Minor: m.GetAmountMinor()}
}

func tariffToProto(t domain.Tariff) *productv1.Tariff {
	out := &productv1.Tariff{
		BasePremium:    moneyToProto(t.BasePremium),
		MinimumPremium: moneyToProto(t.MinimumPremium),
		TaxRateBp:      t.TaxRateBP,
	}
	for _, f := range t.Factors {
		out.Factors = append(out.Factors, &productv1.RatingFactor{Name: f.Name, MultipliersBp: f.MultipliersBP})
	}
	return out
}

func tariffFromProto(t *productv1.Tariff) domain.Tariff {
	out := domain.Tariff{
		BasePremium:    moneyFromProto(t.GetBasePremium()),
		MinimumPremium: moneyFromProto(t.GetMinimumPremium()),
		TaxRateBP:      t.GetTaxRateBp(),
	}
	for _, f := range t.GetFactors() {
		out.Factors = append(out.Factors, domain.RatingFactor{Name: f.GetName(), MultipliersBP: f.GetMultipliersBp()})
	}
	return out
}

func eventToProto(e domain.Event) *productv1.Event {
	return &productv1.Event{
		Sequence:      e.Sequence,
		Type:          e.Type,
		ProductCode:   e.ProductCode,
		VersionNumber: int32(e.VersionNumber),
		Actor:         e.Actor,
		OccurredAt:    timestamppb.New(e.OccurredAt),
	}
}
