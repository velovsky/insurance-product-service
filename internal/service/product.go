// Package service exposes the domain over gRPC. It owns transport concerns
// only: decoding requests, the acting user and error codes.
package service

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	productv1 "github.com/velovsky/insurance-product-service/gen/insurance/product/v1"
	"github.com/velovsky/insurance-product-service/internal/domain"
	"github.com/velovsky/insurance-product-service/internal/store"
)

// Repository is what the service needs from storage; store.Memory satisfies it.
type Repository interface {
	Create(p *domain.Product, actor string, now time.Time) error
	Get(code string) (*domain.Product, error)
	Update(code, actor string, now time.Time, mutate store.Mutation) (*domain.Product, error)
	Events(afterSequence int64, limit int) []domain.Event
}

type Clock func() time.Time

type ProductServer struct {
	productv1.UnimplementedProductServiceServer
	repo Repository
	now  Clock
}

func NewProductServer(repo Repository, now Clock) *ProductServer {
	return &ProductServer{repo: repo, now: now}
}

func (s *ProductServer) CreateProduct(ctx context.Context, req *productv1.CreateProductRequest) (*productv1.CreateProductResponse, error) {
	actor, err := actorFrom(ctx)
	if err != nil {
		return nil, err
	}
	p, err := domain.NewProduct(req.GetCode(), req.GetName(), req.GetLine())
	if err != nil {
		return nil, toStatus(err)
	}
	if err := s.repo.Create(p, actor, s.now()); err != nil {
		return nil, toStatus(err)
	}
	return &productv1.CreateProductResponse{Product: toProto(p)}, nil
}

func (s *ProductServer) GetProduct(_ context.Context, req *productv1.GetProductRequest) (*productv1.GetProductResponse, error) {
	p, err := s.repo.Get(req.GetCode())
	if err != nil {
		return nil, toStatus(err)
	}
	return &productv1.GetProductResponse{Product: toProto(p)}, nil
}

func (s *ProductServer) CreateDraftVersion(ctx context.Context, req *productv1.CreateDraftVersionRequest) (*productv1.CreateDraftVersionResponse, error) {
	actor, err := actorFrom(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetTariff() == nil {
		return nil, status.Error(codes.InvalidArgument, "tariff is required")
	}
	effective, err := domain.ParseDate(req.GetEffectiveFrom())
	if err != nil {
		return nil, toStatus(err)
	}
	now := s.now()
	var created domain.Version
	_, err = s.repo.Update(req.GetProductCode(), actor, now, func(p *domain.Product) (string, int, error) {
		v, err := p.AddDraft(tariffFromProto(req.GetTariff()), effective, req.GetChangeNote(), actor, now)
		created = v
		return domain.EventDraftCreated, v.Number, err
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &productv1.CreateDraftVersionResponse{Version: versionToProto(created)}, nil
}

func (s *ProductServer) ValidateDraft(_ context.Context, req *productv1.ValidateDraftRequest) (*productv1.ValidateDraftResponse, error) {
	p, err := s.repo.Get(req.GetProductCode())
	if err != nil {
		return nil, toStatus(err)
	}
	samples := make([]map[string]string, 0, len(req.GetSampleRisks()))
	for _, r := range req.GetSampleRisks() {
		samples = append(samples, r.GetAttributes())
	}
	res, err := p.DryRun(int(req.GetVersionNumber()), samples, s.now())
	if err != nil {
		return nil, toStatus(err)
	}

	out := &productv1.ValidateDraftResponse{Valid: res.Valid(), ComparedToVersion: int32(res.ComparedToVersion)}
	for _, is := range res.Issues {
		out.Issues = append(out.Issues, &productv1.ValidationIssue{Field: is.Field, Message: is.Message})
	}
	for _, c := range res.Changes {
		out.Changes = append(out.Changes, &productv1.TariffChange{Path: c.Path, Before: c.Before, After: c.After})
	}
	for _, im := range res.Impacts {
		pi := &productv1.SampleImpact{
			Risk:          &productv1.Risk{Attributes: im.Risk},
			ProposedTotal: moneyToProto(im.Proposed),
			ChangeBp:      im.ChangeBP,
		}
		if im.Current != nil {
			pi.CurrentTotal = moneyToProto(*im.Current)
		}
		out.Impacts = append(out.Impacts, pi)
	}
	return out, nil
}

func (s *ProductServer) ApproveVersion(ctx context.Context, req *productv1.ApproveVersionRequest) (*productv1.ApproveVersionResponse, error) {
	v, err := s.transition(ctx, req.GetProductCode(), domain.EventVersionApproved, func(p *domain.Product, actor string, now time.Time) (domain.Version, error) {
		return p.Approve(int(req.GetVersionNumber()), actor, now)
	})
	if err != nil {
		return nil, err
	}
	return &productv1.ApproveVersionResponse{Version: versionToProto(v)}, nil
}

func (s *ProductServer) PublishVersion(ctx context.Context, req *productv1.PublishVersionRequest) (*productv1.PublishVersionResponse, error) {
	v, err := s.transition(ctx, req.GetProductCode(), domain.EventVersionPublished, func(p *domain.Product, _ string, now time.Time) (domain.Version, error) {
		return p.Publish(int(req.GetVersionNumber()), now)
	})
	if err != nil {
		return nil, err
	}
	return &productv1.PublishVersionResponse{Version: versionToProto(v)}, nil
}

func (s *ProductServer) transition(ctx context.Context, code, eventType string, step func(*domain.Product, string, time.Time) (domain.Version, error)) (domain.Version, error) {
	actor, err := actorFrom(ctx)
	if err != nil {
		return domain.Version{}, err
	}
	now := s.now()
	var result domain.Version
	_, err = s.repo.Update(code, actor, now, func(p *domain.Product) (string, int, error) {
		v, err := step(p, actor, now)
		result = v
		return eventType, v.Number, err
	})
	if err != nil {
		return domain.Version{}, toStatus(err)
	}
	return result, nil
}

const (
	defaultEventPage = 100
	maxEventPage     = 1000
)

func (s *ProductServer) ListEvents(_ context.Context, req *productv1.ListEventsRequest) (*productv1.ListEventsResponse, error) {
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultEventPage
	}
	limit = min(limit, maxEventPage)
	out := &productv1.ListEventsResponse{}
	for _, e := range s.repo.Events(req.GetAfterSequence(), limit) {
		out.Events = append(out.Events, eventToProto(e))
	}
	return out, nil
}
