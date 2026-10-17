package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	productv1 "github.com/velovsky/insurance-product-service/gen/insurance/product/v1"
	"github.com/velovsky/insurance-product-service/internal/domain"
)

type QuoteServer struct {
	productv1.UnimplementedQuoteServiceServer
	repo Repository
	now  Clock
}

func NewQuoteServer(repo Repository, now Clock) *QuoteServer {
	return &QuoteServer{repo: repo, now: now}
}

// CreateQuote prices against the version in force on as_of (today if empty)
// and returns that version number, so the quote can be reproduced later.
func (s *QuoteServer) CreateQuote(_ context.Context, req *productv1.CreateQuoteRequest) (*productv1.CreateQuoteResponse, error) {
	asOf := domain.Today(s.now())
	if req.GetAsOf() != "" {
		d, err := domain.ParseDate(req.GetAsOf())
		if err != nil {
			return nil, toStatus(err)
		}
		asOf = d
	}

	p, err := s.repo.Get(req.GetProductCode())
	if err != nil {
		return nil, toStatus(err)
	}
	v, ok := p.InForce(asOf)
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "product %s has no published version in force on %s", p.Code, domain.FormatDate(asOf))
	}
	q, err := domain.Price(v.Tariff, req.GetRisk().GetAttributes())
	if err != nil {
		return nil, toStatus(err)
	}

	out := &productv1.CreateQuoteResponse{
		ProductCode:   p.Code,
		VersionNumber: int32(v.Number),
		NetPremium:    moneyToProto(q.Net),
		Tax:           moneyToProto(q.Tax),
		Total:         moneyToProto(q.Total),
	}
	for _, l := range q.Lines {
		out.Lines = append(out.Lines, &productv1.PremiumLine{Label: l.Label, Amount: moneyToProto(l.Amount)})
	}
	return out, nil
}
