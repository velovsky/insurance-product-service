package agenttools

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	productv1 "github.com/velovsky/insurance-product-service/gen/insurance/product/v1"
	"github.com/velovsky/insurance-product-service/internal/service"
	"github.com/velovsky/insurance-product-service/internal/store"
)

const agent = "agent:test"

var oldPensioner = map[string]string{"age_band": "65+", "destination": "world", "trip_length": "month"}

type fixture struct {
	session  *mcp.ClientSession
	products productv1.ProductServiceClient
}

// newFixture runs the real gRPC service with a published travel product and
// connects an MCP client to the agent tools over an in-memory transport.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	clock := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	repo := store.NewMemory()

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	productv1.RegisterProductServiceServer(grpcSrv, service.NewProductServer(repo, now))
	productv1.RegisterQuoteServiceServer(grpcSrv, service.NewQuoteServer(repo, now))
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	products := productv1.NewProductServiceClient(conn)
	publishTravelProduct(t, products)

	srv, err := NewServer(conn, agent, "test")
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &fixture{session: session, products: products}
}

func publishTravelProduct(t *testing.T, products productv1.ProductServiceClient) {
	t.Helper()
	if _, err := products.CreateProduct(as("alice"), &productv1.CreateProductRequest{Code: "travel-basic", Name: "Travel Basic", Line: "travel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := products.CreateDraftVersion(as("alice"), &productv1.CreateDraftVersionRequest{
		ProductCode:   "travel-basic",
		EffectiveFrom: "2026-10-17",
		ChangeNote:    "Launch tariff",
		Tariff:        tariffIn(launchTariff()),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := products.ApproveVersion(as("bob"), &productv1.ApproveVersionRequest{ProductCode: "travel-basic", VersionNumber: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := products.PublishVersion(as("bob"), &productv1.PublishVersionRequest{ProductCode: "travel-basic", VersionNumber: 1}); err != nil {
		t.Fatal(err)
	}
}

func launchTariff() Tariff {
	return Tariff{
		BasePremium:    Money{Currency: "EUR", AmountMinor: 4900},
		MinimumPremium: Money{Currency: "EUR", AmountMinor: 2500},
		TaxRateBP:      1900,
		Factors: []Factor{
			{Name: "age_band", MultipliersBP: map[string]int64{"18-30": 11000, "31-64": 10000, "65+": 16000}},
			{Name: "destination", MultipliersBP: map[string]int64{"europe": 10000, "world": 14500}},
			{Name: "trip_length", MultipliersBP: map[string]int64{"week": 10000, "month": 18000}},
		},
	}
}

func as(actor string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), service.ActorHeader, actor)
}

// call invokes a tool and decodes its structured result, or returns the tool error text.
func call[T any](t *testing.T, f *fixture, name string, args any) (T, string) {
	t.Helper()
	var out T
	res, err := f.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	if res.IsError {
		return out, res.Content[0].(*mcp.TextContent).Text
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: decoding %s: %v", name, raw, err)
	}
	return out, ""
}

func mustCall[T any](t *testing.T, f *fixture, name string, args any) T {
	t.Helper()
	out, toolErr := call[T](t, f, name, args)
	if toolErr != "" {
		t.Fatalf("%s: tool error: %s", name, toolErr)
	}
	return out
}

func TestAgentsGetNoApproveOrPublishTool(t *testing.T) {
	f := newFixture(t)
	res, err := f.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := map[string]bool{}
	for _, tool := range res.Tools {
		readOnly[tool.Name] = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
	}
	want := map[string]bool{
		"get_product":           true,
		"create_quote":          true,
		"validate_draft":        true,
		"list_events":           true,
		"propose_tariff_change": false,
	}
	if len(readOnly) != len(want) {
		t.Fatalf("tools = %v, want exactly %v", readOnly, want)
	}
	for name, wantRO := range want {
		gotRO, ok := readOnly[name]
		if !ok {
			t.Fatalf("missing tool %s", name)
		}
		if gotRO != wantRO {
			t.Errorf("%s read-only hint = %v, want %v", name, gotRO, wantRO)
		}
	}
}

func TestQuoteShowsEveryStepAndTheVersionUsed(t *testing.T) {
	f := newFixture(t)
	q := mustCall[QuoteOutput](t, f, "create_quote", QuoteInput{ProductCode: "travel-basic", Risk: oldPensioner})
	if q.VersionNumber != 1 {
		t.Errorf("version = %d, want 1", q.VersionNumber)
	}
	if q.Total.Display != "EUR 243.50" {
		t.Errorf("total = %s, want EUR 243.50", q.Total.Display)
	}
	if len(q.Lines) != 5 {
		t.Errorf("lines = %d, want base + 3 factors + tax", len(q.Lines))
	}
}

// TestAgentProposalNeedsAPerson walks the full loop: the agent reads, proposes
// and dry-runs a change, cannot approve it itself, and a person approves it.
func TestAgentProposalNeedsAPerson(t *testing.T) {
	f := newFixture(t)

	product := mustCall[GetProductOutput](t, f, "get_product", GetProductInput{Code: "travel-basic"})
	tariff := product.Versions[0].Tariff
	tariff.Factors[1].MultipliersBP["world"] = 16000

	draft := mustCall[ProposeOutput](t, f, "propose_tariff_change", ProposeInput{
		ProductCode:   "travel-basic",
		EffectiveFrom: "2027-01-01",
		ChangeNote:    "Claims ratio on world cover is up",
		Tariff:        tariff,
	})
	if draft.VersionNumber != 2 || draft.Status != "DRAFT" {
		t.Fatalf("draft = %+v, want version 2 DRAFT", draft)
	}

	dry := mustCall[ValidateOutput](t, f, "validate_draft", ValidateInput{
		ProductCode: "travel-basic", VersionNumber: 2, SampleRisks: []map[string]string{oldPensioner},
	})
	if !dry.Valid || dry.ComparedToVersion != 1 {
		t.Fatalf("dry run = %+v, want valid against version 1", dry)
	}
	if len(dry.Changes) != 1 || dry.Changes[0].Path != "factors.destination.world" {
		t.Fatalf("changes = %+v, want only factors.destination.world", dry.Changes)
	}
	if got := dry.Impacts[0]; got.Current.Display != "EUR 243.50" || got.Proposed.Display != "EUR 268.69" || got.Change != "+10.34%" {
		t.Fatalf("impact = %+v, want EUR 243.50 -> EUR 268.69 (+10.34%%)", got)
	}

	// Even a client that skips the tools and calls the API directly cannot
	// approve its own proposal: the service enforces four-eyes.
	_, err := f.products.ApproveVersion(as(agent), &productv1.ApproveVersionRequest{ProductCode: "travel-basic", VersionNumber: 2})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("agent self-approval: code = %v, want PermissionDenied", status.Code(err))
	}
	if _, err := f.products.ApproveVersion(as("carol"), &productv1.ApproveVersionRequest{ProductCode: "travel-basic", VersionNumber: 2}); err != nil {
		t.Fatalf("person approval: %v", err)
	}

	events := mustCall[ListEventsOutput](t, f, "list_events", ListEventsInput{})
	var draftedBy, approvedBy string
	for _, e := range events.Events {
		if e.VersionNumber != 2 {
			continue
		}
		switch e.Type {
		case "version.draft_created":
			draftedBy = e.Actor
		case "version.approved":
			approvedBy = e.Actor
		}
	}
	if draftedBy != agent || approvedBy != "carol" {
		t.Fatalf("audit log: drafted by %q, approved by %q; want %q and carol (events: %+v)", draftedBy, approvedBy, agent, events.Events)
	}
}

func TestGuardrailFlagsAnOutOfRangeProposal(t *testing.T) {
	f := newFixture(t)
	tariff := launchTariff()
	tariff.Factors[1].MultipliersBP["world"] = 60000

	draft := mustCall[ProposeOutput](t, f, "propose_tariff_change", ProposeInput{
		ProductCode: "travel-basic", EffectiveFrom: "2027-01-01", ChangeNote: "Triple world cover", Tariff: tariff,
	})
	dry := mustCall[ValidateOutput](t, f, "validate_draft", ValidateInput{ProductCode: "travel-basic", VersionNumber: draft.VersionNumber})
	if dry.Valid {
		t.Fatal("dry run is valid, want the x6 multiplier rejected")
	}
	if !strings.Contains(dry.Issues[0].Message, "outside the allowed range") {
		t.Fatalf("issues = %+v, want an out-of-range issue", dry.Issues)
	}
}

func TestToolErrorsTellTheAgentWhatToFix(t *testing.T) {
	f := newFixture(t)
	_, toolErr := call[ProposeOutput](t, f, "propose_tariff_change", ProposeInput{
		ProductCode: "travel-basic", EffectiveFrom: "2027-01-01", Tariff: launchTariff(),
	})
	if !strings.Contains(toolErr, "change_note is required") {
		t.Errorf("missing note: error = %q", toolErr)
	}

	_, toolErr = call[GetProductOutput](t, f, "get_product", GetProductInput{Code: "does-not-exist"})
	if !strings.HasPrefix(toolErr, "not_found:") {
		t.Errorf("unknown product: error = %q, want a not_found error", toolErr)
	}
}

func TestServerRequiresAnAgentActor(t *testing.T) {
	for _, actor := range []string{"alice", "agent:", ""} {
		if _, err := NewServer(nil, actor, "test"); err == nil {
			t.Errorf("NewServer(%q) succeeded, want an error", actor)
		}
	}
}
