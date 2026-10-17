package service

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	productv1 "github.com/velovsky/insurance-product-service/gen/insurance/product/v1"
	"github.com/velovsky/insurance-product-service/internal/store"
)

type fixture struct {
	products productv1.ProductServiceClient
	quotes   productv1.QuoteServiceClient
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clock := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	repo := store.NewMemory()

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	productv1.RegisterProductServiceServer(srv, NewProductServer(repo, now))
	productv1.RegisterQuoteServiceServer(srv, NewQuoteServer(repo, now))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &fixture{
		products: productv1.NewProductServiceClient(conn),
		quotes:   productv1.NewQuoteServiceClient(conn),
	}
}

func as(actor string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), ActorHeader, actor)
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("code = %v, want %v (err: %v)", got, want, err)
	}
}

func eur(minor int64) *productv1.Money { return &productv1.Money{Currency: "EUR", AmountMinor: minor} }

func travelTariff(world int64) *productv1.Tariff {
	return &productv1.Tariff{
		BasePremium: eur(10000),
		Factors: []*productv1.RatingFactor{
			{Name: "age_band", MultipliersBp: map[string]int64{"18-30": 12000, "31-64": 10000}},
			{Name: "destination", MultipliersBp: map[string]int64{"europe": 10000, "world": world}},
		},
		MinimumPremium: eur(5000),
		TaxRateBp:      1900,
	}
}

var youngWorld = &productv1.Risk{Attributes: map[string]string{"age_band": "18-30", "destination": "world"}}

func TestAgentProposalLifecycle(t *testing.T) {
	f := newFixture(t)
	const code = "travel-basic"

	if _, err := f.products.CreateProduct(context.Background(), &productv1.CreateProductRequest{Code: code, Name: "Travel", Line: "travel"}); err == nil {
		t.Fatal("CreateProduct without an actor must fail")
	}
	if _, err := f.products.CreateProduct(as("alice"), &productv1.CreateProductRequest{Code: code, Name: "Travel Basic", Line: "travel"}); err != nil {
		t.Fatal(err)
	}

	// v1: alice proposes, bob approves and publishes.
	draft, err := f.products.CreateDraftVersion(as("alice"), &productv1.CreateDraftVersionRequest{
		ProductCode: code, EffectiveFrom: "2026-11-01", Tariff: travelTariff(14000), ChangeNote: "launch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.products.ApproveVersion(as("bob"), &productv1.ApproveVersionRequest{ProductCode: code, VersionNumber: draft.GetVersion().GetNumber()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.products.PublishVersion(as("bob"), &productv1.PublishVersionRequest{ProductCode: code, VersionNumber: 1}); err != nil {
		t.Fatal(err)
	}

	// v2: an agent proposes a higher world multiplier for next year.
	agent := as("agent:pricing-bot")
	if _, err := f.products.CreateDraftVersion(agent, &productv1.CreateDraftVersionRequest{
		ProductCode: code, EffectiveFrom: "2027-01-01", Tariff: travelTariff(15000), ChangeNote: "claims ratio up on world cover",
	}); err != nil {
		t.Fatal(err)
	}

	dry, err := f.products.ValidateDraft(context.Background(), &productv1.ValidateDraftRequest{
		ProductCode: code, VersionNumber: 2, SampleRisks: []*productv1.Risk{youngWorld},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dry.GetValid() || dry.GetComparedToVersion() != 1 || len(dry.GetChanges()) != 1 {
		t.Fatalf("dry run = %+v", dry)
	}
	impact := dry.GetImpacts()[0]
	if impact.GetCurrentTotal().GetAmountMinor() != 19992 || impact.GetProposedTotal().GetAmountMinor() != 21420 {
		t.Errorf("impact = %+v", impact)
	}

	_, err = f.products.ApproveVersion(agent, &productv1.ApproveVersionRequest{ProductCode: code, VersionNumber: 2})
	wantCode(t, err, codes.PermissionDenied)

	if _, err := f.products.ApproveVersion(as("bob"), &productv1.ApproveVersionRequest{ProductCode: code, VersionNumber: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.products.PublishVersion(as("bob"), &productv1.PublishVersionRequest{ProductCode: code, VersionNumber: 2}); err != nil {
		t.Fatal(err)
	}

	// Quotes use the version in force on the requested date.
	for _, tt := range []struct {
		asOf        string
		wantVersion int32
		wantTotal   int64
	}{
		{"2026-12-31", 1, 19992},
		{"2027-01-01", 2, 21420},
	} {
		q, err := f.quotes.CreateQuote(context.Background(), &productv1.CreateQuoteRequest{ProductCode: code, AsOf: tt.asOf, Risk: youngWorld})
		if err != nil {
			t.Fatal(err)
		}
		if q.GetVersionNumber() != tt.wantVersion || q.GetTotal().GetAmountMinor() != tt.wantTotal {
			t.Errorf("quote as of %s = v%d %d, want v%d %d", tt.asOf, q.GetVersionNumber(), q.GetTotal().GetAmountMinor(), tt.wantVersion, tt.wantTotal)
		}
	}
	_, err = f.quotes.CreateQuote(context.Background(), &productv1.CreateQuoteRequest{ProductCode: code, AsOf: "2026-10-31", Risk: youngWorld})
	wantCode(t, err, codes.FailedPrecondition)

	events, err := f.products.ListEvents(context.Background(), &productv1.ListEventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range events.GetEvents() {
		types = append(types, e.GetActor()+" "+e.GetType())
	}
	want := []string{
		"alice product.created",
		"alice version.draft_created",
		"bob version.approved",
		"bob version.published",
		"agent:pricing-bot version.draft_created",
		"bob version.approved",
		"bob version.published",
	}
	if len(types) != len(want) {
		t.Fatalf("events = %v", types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("event %d = %q, want %q (rejected self-approval must not be logged)", i, types[i], want[i])
		}
	}
}

func TestValidationErrorsAreMachineReadable(t *testing.T) {
	f := newFixture(t)
	if _, err := f.products.CreateProduct(as("alice"), &productv1.CreateProductRequest{Code: "travel-basic", Name: "Travel Basic", Line: "travel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.products.CreateDraftVersion(as("agent:pricing-bot"), &productv1.CreateDraftVersionRequest{
		ProductCode: "travel-basic", EffectiveFrom: "2026-11-01", Tariff: travelTariff(140000), // 14x typo
	}); err != nil {
		t.Fatal(err)
	}

	_, err := f.products.ApproveVersion(as("bob"), &productv1.ApproveVersionRequest{ProductCode: "travel-basic", VersionNumber: 1})
	wantCode(t, err, codes.InvalidArgument)

	st, _ := status.FromError(err)
	var fields []string
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			for _, v := range br.GetFieldViolations() {
				fields = append(fields, v.GetField())
			}
		}
	}
	if len(fields) != 1 || fields[0] != "tariff.factors[1].multipliers_bp.world" {
		t.Errorf("field violations = %v", fields)
	}
}

func TestQuoteRejectsUnknownProduct(t *testing.T) {
	f := newFixture(t)
	_, err := f.quotes.CreateQuote(context.Background(), &productv1.CreateQuoteRequest{ProductCode: "nope", Risk: youngWorld})
	wantCode(t, err, codes.NotFound)
}
