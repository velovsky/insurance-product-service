// Package agenttools exposes the product API to AI agents as MCP tools.
//
// Agents can read products, price risks, propose a new tariff and dry-run it.
// There is deliberately no tool to approve or publish: those stay with people.
// The service enforces the same boundary again with its four-eyes rule, so a
// client that bypasses this package still cannot approve its own proposal.
package agenttools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	productv1 "github.com/velovsky/insurance-product-service/gen/insurance/product/v1"
	"github.com/velovsky/insurance-product-service/internal/service"
)

// AgentPrefix marks every change made through these tools in the audit log.
const AgentPrefix = "agent:"

type tools struct {
	products productv1.ProductServiceClient
	quotes   productv1.QuoteServiceClient
	actor    string
}

// NewServer returns an MCP server whose tools call the gRPC API as actor.
func NewServer(conn grpc.ClientConnInterface, actor, version string) (*mcp.Server, error) {
	if !strings.HasPrefix(actor, AgentPrefix) || len(actor) == len(AgentPrefix) {
		return nil, fmt.Errorf("actor %q must start with %q, so agent changes are visible in the audit log", actor, AgentPrefix)
	}
	t := &tools{
		products: productv1.NewProductServiceClient(conn),
		quotes:   productv1.NewQuoteServiceClient(conn),
		actor:    actor,
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "insurance-product-service", Version: version}, &mcp.ServerOptions{
		Instructions: "Tools for insurance tariffs. Read a product with get_product before you change it. " +
			"A change is only a proposal: propose_tariff_change creates a draft, validate_draft shows its issues, diff and price impact. " +
			"You cannot approve or publish; a person reviews every draft.",
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_product",
		Description: "Get a product with all its tariff versions (status, effective date, multipliers, author and approver).",
		Annotations: readOnly("Get product"),
	}, t.getProduct)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_quote",
		Description: "Price a risk with the tariff version in force on a date. Returns every pricing step and the version used.",
		Annotations: readOnly("Create quote"),
	}, t.createQuote)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "propose_tariff_change",
		Description: "Propose a new tariff version as a draft. Copy the current tariff from get_product and change only what you need. " +
			"The draft has no effect until a person approves and publishes it. A product can have only one open draft.",
		Annotations: &mcp.ToolAnnotations{Title: "Propose tariff change", DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)},
	}, t.proposeTariffChange)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "validate_draft",
		Description: "Dry-run a draft: validation issues, the diff against the version it would replace, and the price change for sample risks. Changes nothing.",
		Annotations: readOnly("Validate draft"),
	}, t.validateDraft)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_events",
		Description: "Read the audit log in order: who created, approved or published which version, and when.",
		Annotations: readOnly("List events"),
	}, t.listEvents)

	return srv, nil
}

// --- inputs and outputs -------------------------------------------------------

type Money struct {
	Currency    string `json:"currency" jsonschema:"ISO 4217 code, e.g. EUR"`
	AmountMinor int64  `json:"amount_minor" jsonschema:"amount in minor units (cents)"`
	Display     string `json:"display,omitempty" jsonschema:"human-readable amount, ignored on input"`
}

type Tariff struct {
	BasePremium    Money    `json:"base_premium"`
	MinimumPremium Money    `json:"minimum_premium"`
	TaxRateBP      int64    `json:"tax_rate_bp" jsonschema:"premium tax in basis points, 1900 = 19%"`
	Factors        []Factor `json:"factors" jsonschema:"rating factors, applied in this order"`
}

type Factor struct {
	Name          string           `json:"name" jsonschema:"risk attribute, e.g. age_band"`
	MultipliersBP map[string]int64 `json:"multipliers_bp" jsonschema:"option -> multiplier in basis points (10000 = x1.0, allowed 1000-50000)"`
}

type Version struct {
	Number        int32  `json:"number"`
	Status        string `json:"status" jsonschema:"DRAFT, APPROVED or PUBLISHED"`
	EffectiveFrom string `json:"effective_from"`
	ChangeNote    string `json:"change_note,omitempty"`
	CreatedBy     string `json:"created_by"`
	ApprovedBy    string `json:"approved_by,omitempty"`
	Tariff        Tariff `json:"tariff"`
}

type GetProductInput struct {
	Code string `json:"code" jsonschema:"product code, e.g. travel-basic"`
}

type GetProductOutput struct {
	Code     string    `json:"code"`
	Name     string    `json:"name"`
	Line     string    `json:"line"`
	Versions []Version `json:"versions"`
}

type QuoteInput struct {
	ProductCode string            `json:"product_code"`
	AsOf        string            `json:"as_of,omitempty" jsonschema:"date YYYY-MM-DD whose version in force is used; today if empty"`
	Risk        map[string]string `json:"risk" jsonschema:"rating factor -> chosen option, e.g. {\"age_band\": \"65+\"}"`
}

type QuoteLine struct {
	Label  string `json:"label"`
	Amount Money  `json:"amount"`
}

type QuoteOutput struct {
	VersionNumber int32       `json:"version_number" jsonschema:"tariff version used, so the quote can be reproduced"`
	Lines         []QuoteLine `json:"lines"`
	Net           Money       `json:"net_premium"`
	Tax           Money       `json:"tax"`
	Total         Money       `json:"total"`
}

type ProposeInput struct {
	ProductCode   string `json:"product_code"`
	EffectiveFrom string `json:"effective_from" jsonschema:"date YYYY-MM-DD from which the new version applies; must be after the current version"`
	ChangeNote    string `json:"change_note" jsonschema:"why the tariff changes, for the person who reviews it"`
	Tariff        Tariff `json:"tariff"`
}

type ProposeOutput struct {
	VersionNumber int32  `json:"version_number"`
	Status        string `json:"status"`
	NextStep      string `json:"next_step"`
}

type ValidateInput struct {
	ProductCode   string              `json:"product_code"`
	VersionNumber int32               `json:"version_number"`
	SampleRisks   []map[string]string `json:"sample_risks,omitempty" jsonschema:"risks to price under the current and the proposed tariff"`
}

type Issue struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type Change struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type Impact struct {
	Risk     map[string]string `json:"risk"`
	Current  *Money            `json:"current_total,omitempty"`
	Proposed Money             `json:"proposed_total"`
	Change   string            `json:"change" jsonschema:"relative change, e.g. +10.34%"`
}

type ValidateOutput struct {
	Valid             bool     `json:"valid"`
	Issues            []Issue  `json:"issues"`
	ComparedToVersion int32    `json:"compared_to_version"`
	Changes           []Change `json:"changes"`
	Impacts           []Impact `json:"impacts"`
}

type ListEventsInput struct {
	AfterSequence int64 `json:"after_sequence,omitempty"`
	Limit         int32 `json:"limit,omitempty" jsonschema:"maximum number of events, default 50"`
}

type Event struct {
	Sequence      int64  `json:"sequence"`
	Type          string `json:"type"`
	ProductCode   string `json:"product_code"`
	VersionNumber int32  `json:"version_number,omitempty"`
	Actor         string `json:"actor"`
	OccurredAt    string `json:"occurred_at"`
}

type ListEventsOutput struct {
	Events []Event `json:"events"`
}

// --- handlers -----------------------------------------------------------------

func (t *tools) getProduct(ctx context.Context, _ *mcp.CallToolRequest, in GetProductInput) (*mcp.CallToolResult, GetProductOutput, error) {
	res, err := t.products.GetProduct(t.as(ctx), &productv1.GetProductRequest{Code: in.Code})
	if err != nil {
		return nil, GetProductOutput{}, explain(err)
	}
	p := res.GetProduct()
	out := GetProductOutput{Code: p.GetCode(), Name: p.GetName(), Line: p.GetLine(), Versions: []Version{}}
	for _, v := range p.GetVersions() {
		out.Versions = append(out.Versions, versionOut(v))
	}
	return nil, out, nil
}

func (t *tools) createQuote(ctx context.Context, _ *mcp.CallToolRequest, in QuoteInput) (*mcp.CallToolResult, QuoteOutput, error) {
	res, err := t.quotes.CreateQuote(t.as(ctx), &productv1.CreateQuoteRequest{
		ProductCode: in.ProductCode,
		AsOf:        in.AsOf,
		Risk:        &productv1.Risk{Attributes: in.Risk},
	})
	if err != nil {
		return nil, QuoteOutput{}, explain(err)
	}
	out := QuoteOutput{
		VersionNumber: res.GetVersionNumber(),
		Lines:         []QuoteLine{},
		Net:           moneyOut(res.GetNetPremium()),
		Tax:           moneyOut(res.GetTax()),
		Total:         moneyOut(res.GetTotal()),
	}
	for _, l := range res.GetLines() {
		out.Lines = append(out.Lines, QuoteLine{Label: l.GetLabel(), Amount: moneyOut(l.GetAmount())})
	}
	return nil, out, nil
}

func (t *tools) proposeTariffChange(ctx context.Context, _ *mcp.CallToolRequest, in ProposeInput) (*mcp.CallToolResult, ProposeOutput, error) {
	if strings.TrimSpace(in.ChangeNote) == "" {
		return nil, ProposeOutput{}, errors.New("change_note is required: explain the change for the person who reviews it")
	}
	res, err := t.products.CreateDraftVersion(t.as(ctx), &productv1.CreateDraftVersionRequest{
		ProductCode:   in.ProductCode,
		EffectiveFrom: in.EffectiveFrom,
		ChangeNote:    in.ChangeNote,
		Tariff:        tariffIn(in.Tariff),
	})
	if err != nil {
		return nil, ProposeOutput{}, explain(err)
	}
	v := res.GetVersion()
	return nil, ProposeOutput{
		VersionNumber: v.GetNumber(),
		Status:        statusName(v.GetStatus()),
		NextStep: fmt.Sprintf("Run validate_draft for version %d and fix any issues. A person must approve and publish it; you cannot.",
			v.GetNumber()),
	}, nil
}

func (t *tools) validateDraft(ctx context.Context, _ *mcp.CallToolRequest, in ValidateInput) (*mcp.CallToolResult, ValidateOutput, error) {
	req := &productv1.ValidateDraftRequest{ProductCode: in.ProductCode, VersionNumber: in.VersionNumber}
	for _, r := range in.SampleRisks {
		req.SampleRisks = append(req.SampleRisks, &productv1.Risk{Attributes: r})
	}
	res, err := t.products.ValidateDraft(t.as(ctx), req)
	if err != nil {
		return nil, ValidateOutput{}, explain(err)
	}
	out := ValidateOutput{
		Valid:             res.GetValid(),
		Issues:            []Issue{},
		ComparedToVersion: res.GetComparedToVersion(),
		Changes:           []Change{},
		Impacts:           []Impact{},
	}
	for _, is := range res.GetIssues() {
		out.Issues = append(out.Issues, Issue{Field: is.GetField(), Message: is.GetMessage()})
	}
	for _, c := range res.GetChanges() {
		out.Changes = append(out.Changes, Change{Path: c.GetPath(), Before: c.GetBefore(), After: c.GetAfter()})
	}
	for _, im := range res.GetImpacts() {
		impact := Impact{
			Risk:     im.GetRisk().GetAttributes(),
			Proposed: moneyOut(im.GetProposedTotal()),
			Change:   formatChangeBP(im.GetChangeBp()),
		}
		if im.GetCurrentTotal() != nil {
			cur := moneyOut(im.GetCurrentTotal())
			impact.Current = &cur
		}
		out.Impacts = append(out.Impacts, impact)
	}
	return nil, out, nil
}

func (t *tools) listEvents(ctx context.Context, _ *mcp.CallToolRequest, in ListEventsInput) (*mcp.CallToolResult, ListEventsOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	res, err := t.products.ListEvents(t.as(ctx), &productv1.ListEventsRequest{AfterSequence: in.AfterSequence, Limit: limit})
	if err != nil {
		return nil, ListEventsOutput{}, explain(err)
	}
	out := ListEventsOutput{Events: []Event{}}
	for _, e := range res.GetEvents() {
		out.Events = append(out.Events, Event{
			Sequence:      e.GetSequence(),
			Type:          e.GetType(),
			ProductCode:   e.GetProductCode(),
			VersionNumber: e.GetVersionNumber(),
			Actor:         e.GetActor(),
			OccurredAt:    e.GetOccurredAt().AsTime().UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return nil, out, nil
}

// --- helpers ------------------------------------------------------------------

func (t *tools) as(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, service.ActorHeader, t.actor)
}

// explain turns a gRPC error into one message an agent can act on, including
// every field violation from the BadRequest detail.
func explain(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", snakeCase(st.Code().String()), st.Message())
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			for _, v := range br.GetFieldViolations() {
				fmt.Fprintf(&b, "\n- %s: %s", v.GetField(), v.GetDescription())
			}
		}
	}
	return errors.New(b.String())
}

// snakeCase turns a gRPC code name like "PermissionDenied" into "permission_denied".
func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(false)}
}

func boolPtr(b bool) *bool { return &b }

func moneyOut(m *productv1.Money) Money {
	if m == nil {
		return Money{}
	}
	return Money{
		Currency:    m.GetCurrency(),
		AmountMinor: m.GetAmountMinor(),
		Display:     formatMinor(m.GetCurrency(), m.GetAmountMinor()),
	}
}

func formatMinor(currency string, minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s %s%d.%02d", currency, sign, minor/100, minor%100)
}

func formatChangeBP(bp int64) string {
	sign := "+"
	if bp < 0 {
		sign, bp = "-", -bp
	}
	return fmt.Sprintf("%s%d.%02d%%", sign, bp/100, bp%100)
}

func statusName(s productv1.VersionStatus) string {
	return strings.TrimPrefix(s.String(), "VERSION_STATUS_")
}

func versionOut(v *productv1.ProductVersion) Version {
	return Version{
		Number:        v.GetNumber(),
		Status:        statusName(v.GetStatus()),
		EffectiveFrom: v.GetEffectiveFrom(),
		ChangeNote:    v.GetChangeNote(),
		CreatedBy:     v.GetCreatedBy(),
		ApprovedBy:    v.GetApprovedBy(),
		Tariff:        tariffOut(v.GetTariff()),
	}
}

func tariffOut(t *productv1.Tariff) Tariff {
	out := Tariff{
		BasePremium:    moneyOut(t.GetBasePremium()),
		MinimumPremium: moneyOut(t.GetMinimumPremium()),
		TaxRateBP:      t.GetTaxRateBp(),
		Factors:        []Factor{},
	}
	for _, f := range t.GetFactors() {
		out.Factors = append(out.Factors, Factor{Name: f.GetName(), MultipliersBP: f.GetMultipliersBp()})
	}
	return out
}

func tariffIn(t Tariff) *productv1.Tariff {
	out := &productv1.Tariff{
		BasePremium:    &productv1.Money{Currency: t.BasePremium.Currency, AmountMinor: t.BasePremium.AmountMinor},
		MinimumPremium: &productv1.Money{Currency: t.MinimumPremium.Currency, AmountMinor: t.MinimumPremium.AmountMinor},
		TaxRateBp:      t.TaxRateBP,
	}
	for _, f := range t.Factors {
		out.Factors = append(out.Factors, &productv1.RatingFactor{Name: f.Name, MultipliersBp: f.MultipliersBP})
	}
	return out
}
