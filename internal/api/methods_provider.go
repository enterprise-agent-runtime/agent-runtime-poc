package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/creachadair/jrpc2"

	"warden.dev/warden/internal/config"
	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/secrets"
	"warden.dev/warden/internal/store"
)

const ts3 = "2006-01-02T15:04:05.000Z"

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Server) lastTest(id string) (any, *model.ProbeResult) {
	r, err := s.d.Cache.Load(id)
	if err != nil || r == nil || r.ProbedAt.IsZero() {
		return nil, nil
	}
	var code any
	if r.Error != nil {
		code = string(r.Error.Code)
	}
	return map[string]any{"at": r.ProbedAt.UTC().Format(ts3), "ok": r.OK, "latency_ms": r.LatencyMS, "error_code": code}, r
}

func (s *Server) providerView(ctx context.Context, p config.Provider) map[string]any {
	models := []string{}
	for _, m := range s.d.Catalog.ModelsOf(p.ID) {
		models = append(models, m.ID)
	}
	present := p.Auth.Secret == "" || s.d.Broker.Has(ctx, p.Auth.Secret)
	last, r := s.lastTest(p.ID)
	status := "untested"
	switch {
	case !p.IsEnabled():
		status = "disabled"
	case !present:
		status = "credential_missing"
	case r != nil && r.OK:
		status = "ok"
	case r != nil:
		status = "failing"
	}
	return map[string]any{"provider_id": p.ID, "protocol": p.Protocol, "base_url": p.BaseURL, "tier": p.Tier,
		"auth_mode": p.Auth.Mode, "auth_kind": nullable(p.Auth.Kind), "auth_header": nullable(p.Auth.Header),
		"secret_ref": nullable(p.Auth.Secret), "secret_present": p.Auth.Secret != "" && present, "enabled": p.IsEnabled(),
		"status": status, "models": models, "last_test": last}
}

func (s *Server) harnessView(h config.Harness) map[string]any {
	billingMode := "harness_subscription"
	if h.Billing == "api_key" {
		billingMode = "api_key"
	}
	runMode := "colocated"
	if h.Kind == "copilot-sdk" {
		runMode = "split"
	}
	status, locked := "untested", any(nil)
	switch {
	case h.VendorTerms == "prohibited":
		status, locked = "locked", "prohibited"
	case h.VendorTerms == "personal_use_only" && s.d.Mode == "shared":
		status, locked = "locked", "harness_locked_shared_mode"
	case !h.Enabled:
		status = "disabled"
	}
	return map[string]any{"harness_id": h.ID, "kind": h.Kind, "tier": "T4", "billing": h.Billing, "billing_mode": billingMode,
		"vendor_terms": h.VendorTerms, "enabled": h.Enabled, "run_mode": runMode, "status": status, "locked_reason": locked, "last_test": nil}
}

func (s *Server) providerList(ctx context.Context, _ *conn, _ *jrpc2.Request, _ json.RawMessage) (any, error) {
	s.catMu.Lock()
	defer s.catMu.Unlock()
	ps, hs := []any{}, []any{}
	for _, p := range s.d.Catalog.Providers {
		ps = append(ps, s.providerView(ctx, p))
	}
	for _, h := range s.d.Catalog.Harnesses {
		hs = append(hs, s.harnessView(h))
	}
	return map[string]any{"providers": ps, "harnesses": hs, "mode": s.d.Mode}, nil
}

type modelEntrySpec struct {
	ID           string              `json:"id"`
	Provider     string              `json:"provider"`
	Model        string              `json:"model"`
	Capabilities config.Capabilities `json:"capabilities"`
	Pricing      *config.Pricing     `json:"pricing"`
	QualityPrior map[string]float64  `json:"quality_prior"`
}

type addSpec struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
	Auth     struct {
		Mode   string `json:"mode"`
		Kind   string `json:"kind"`
		Header string `json:"header"`
		Secret string `json:"secret"`
	} `json:"auth"`
	Tier   string           `json:"tier"`
	Models []modelEntrySpec `json:"models"`
	// harnessSpec fields
	Kind        string `json:"kind"`
	Billing     string `json:"billing"`
	VendorTerms string `json:"vendor_terms"`
	Enabled     *bool  `json:"enabled"`
}

func (s *Server) configured(ctx context.Context, id, action, tier string, protocol, kind, authMode, secretRef any, result map[string]any) {
	_, _ = s.Append(ctx, store.Input{Type: "provider.configured", Chain: store.SysChain, Actor: s.runtimeActor(), Payload: map[string]any{
		"provider_id": id, "action": action, "tier": tier, "protocol": protocol, "kind": kind, "auth_mode": authMode,
		"secret_ref": secretRef, "result": result}})
}

func secretError(err error) error {
	switch {
	case errors.Is(err, secrets.ErrLocked):
		return Fail(CodeInvalidState, "secrets_locked", "the secrets backend is locked; run warden unlock")
	case errors.Is(err, secrets.ErrUnavailable), errors.Is(err, secrets.ErrTimeout):
		return Fail(CodeInvalidState, "keychain_unavailable", err.Error())
	}
	return Fail(CodeInvalidParams, "secret", err.Error())
}

// providerAdd is provider.add (A05 §8.8): writes models.yaml and the
// keychain item; the value is never echoed, logged or put in an event.
func (s *Server) providerAdd(ctx context.Context, c *conn, req *jrpc2.Request, raw json.RawMessage) (any, error) {
	var p struct {
		Spec        addSpec                 `json:"spec"`
		Secret      *struct{ Value string } `json:"secret"`
		SecretFiles *struct {
			Cert string `json:"cert"`
			Key  string `json:"key"`
		} `json:"secret_files"`
		Test *bool `json:"test"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, Fail(CodeInvalidParams, "", err.Error())
	}
	sp := p.Spec
	if sp.Kind != "" { // harnessSpec
		h := config.Harness{ID: sp.ID, Kind: sp.Kind, Billing: sp.Billing, VendorTerms: sp.VendorTerms, Tier: "T4"}
		if sp.Enabled != nil {
			h.Enabled = *sp.Enabled
		}
		s.catMu.Lock()
		next := *s.d.Catalog
		next.Harnesses = append(append([]config.Harness{}, withoutHarness(next.Harnesses, h.ID)...), h)
		err := next.Save(s.d.CatalogPath)
		if err == nil {
			*s.d.Catalog = next
		}
		s.catMu.Unlock()
		if err != nil {
			return nil, Fail(CodeInvalidParams, "catalog", err.Error())
		}
		s.configured(ctx, h.ID, "add", "T4", nil, h.Kind, "harness_subscription", nil, map[string]any{"ok": true})
		return map[string]any{"provider_id": h.ID, "test": nil}, nil
	}
	prov := config.Provider{ID: sp.ID, Protocol: sp.Protocol, BaseURL: sp.BaseURL, Tier: sp.Tier,
		Auth: config.Auth{Mode: sp.Auth.Mode, Kind: sp.Auth.Kind, Header: sp.Auth.Header, Secret: sp.Auth.Secret}}
	want := map[string]string{model.AuthAPIKey: "api_key", model.KindBearer: "token", model.KindMTLS: "client_key"}
	key := prov.Auth.Mode
	if key == model.AuthGateway {
		key = prov.Auth.Kind
	}
	if prov.Auth.Secret == "" && want[key] != "" {
		prov.Auth.Secret = secrets.ProviderRef(prov.ID, want[key]) // canonical reference (A05)
	}
	if err := prov.Validate(); err != nil {
		code := CodeInvalidParams
		if errors.Is(err, config.ErrUnsupported) {
			code = CodeUnsupportedInPoC
		}
		return nil, Fail(code, "spec", err.Error())
	}
	switch {
	case p.Secret != nil:
		if prov.Auth.Secret == "" || prov.Auth.Kind == model.KindMTLS {
			return nil, Fail(CodeInvalidParams, "secret", "this auth mode takes no secret value")
		}
		if err := s.d.Broker.Put(ctx, prov.Auth.Secret, []byte(p.Secret.Value)); err != nil {
			return nil, secretError(err)
		}
	case p.SecretFiles != nil:
		if prov.Auth.Kind != model.KindMTLS {
			return nil, Fail(CodeInvalidParams, "secret_files", "secret_files are for gateway kind mtls")
		}
		keyPEM, err := os.ReadFile(p.SecretFiles.Key)
		if err != nil {
			return nil, Fail(CodeInvalidParams, "secret_files", "cannot read the key file")
		}
		certPEM, err := os.ReadFile(p.SecretFiles.Cert)
		if err != nil {
			return nil, Fail(CodeInvalidParams, "secret_files", "cannot read the certificate file")
		}
		// The key is copied into the keychain; its file path is never stored.
		if err := s.d.Broker.Put(ctx, secrets.ProviderRef(prov.ID, "client_key"), keyPEM); err != nil {
			return nil, secretError(err)
		}
		if err := s.d.Broker.Put(ctx, secrets.ProviderRef(prov.ID, "client_cert"), certPEM); err != nil {
			return nil, secretError(err)
		}
	case prov.Auth.Secret != "" && !s.d.Broker.Has(ctx, prov.Auth.Secret):
		return nil, Fail(CodeInvalidParams, "secret_missing", "this auth mode needs a secret: pass it with the request (it is stored in the keychain)")
	}
	s.catMu.Lock()
	next := config.Catalog{Providers: append(withoutProvider(s.d.Catalog.Providers, prov.ID), prov), Harnesses: s.d.Catalog.Harnesses}
	keep := s.d.Catalog.Models
	if len(sp.Models) > 0 {
		keep = withoutModelsOf(keep, prov.ID)
		for _, m := range sp.Models {
			keep = append(keep, config.Model{ID: m.ID, Provider: prov.ID, Model: m.Model, Capabilities: m.Capabilities, Pricing: m.Pricing, QualityPrior: m.QualityPrior})
		}
	}
	next.Models = keep
	err := next.Save(s.d.CatalogPath)
	if err == nil {
		*s.d.Catalog = next
	}
	s.catMu.Unlock()
	if err != nil {
		return nil, Fail(CodeInvalidParams, "catalog", err.Error())
	}
	s.d.Cache.Remove(prov.ID)
	s.d.Broker.SetProvider(prov.ID, secrets.ProviderAuth{Protocol: prov.Protocol, Auth: authOf(prov)})
	s.configured(ctx, prov.ID, "add", prov.Tier, prov.Protocol, nil, prov.Auth.Mode, nullable(prov.Auth.Secret), map[string]any{"ok": true})
	var test any
	if p.Test == nil || *p.Test {
		tctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		res, err := s.runTest(tctx, prov.ID)
		if err == nil {
			test = res
		}
	}
	return map[string]any{"provider_id": prov.ID, "test": test}, nil
}

func withoutProvider(ps []config.Provider, id string) []config.Provider {
	var out []config.Provider
	for _, p := range ps {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return out
}

func withoutHarness(hs []config.Harness, id string) []config.Harness {
	var out []config.Harness
	for _, h := range hs {
		if h.ID != id {
			out = append(out, h)
		}
	}
	return out
}

func withoutModelsOf(ms []config.Model, provider string) []config.Model {
	var out []config.Model
	for _, m := range ms {
		if m.Provider != provider {
			out = append(out, m)
		}
	}
	return out
}

func (s *Server) providerRemove(ctx context.Context, _ *conn, _ *jrpc2.Request, raw json.RawMessage) (any, error) {
	var p struct {
		ProviderID string `json:"provider_id"`
	}
	_ = json.Unmarshal(raw, &p)
	s.catMu.Lock()
	prov := s.d.Catalog.Provider(p.ProviderID)
	h := s.d.Catalog.Harness(p.ProviderID)
	if prov == nil && h == nil {
		s.catMu.Unlock()
		return nil, Fail(CodeNotFound, "provider", "unknown provider "+p.ProviderID)
	}
	removed := []string{}
	for _, m := range s.d.Catalog.ModelsOf(p.ProviderID) {
		removed = append(removed, m.ID)
	}
	next := config.Catalog{Providers: withoutProvider(s.d.Catalog.Providers, p.ProviderID), Harnesses: withoutHarness(s.d.Catalog.Harnesses, p.ProviderID),
		Models: withoutModelsOf(s.d.Catalog.Models, p.ProviderID)}
	err := next.Save(s.d.CatalogPath)
	var gone config.Provider
	if prov != nil {
		gone = *prov
	}
	if err == nil {
		*s.d.Catalog = next
	}
	s.catMu.Unlock()
	if err != nil {
		return nil, Fail(CodeInternal, "catalog", err.Error())
	}
	s.d.Cache.Remove(p.ProviderID)
	if prov != nil {
		for _, name := range []string{"api_key", "token", "client_key", "client_cert"} {
			_ = s.d.Broker.Delete(ctx, secrets.ProviderRef(gone.ID, name))
		}
		s.configured(ctx, gone.ID, "remove", gone.Tier, gone.Protocol, nil, gone.Auth.Mode, nullable(gone.Auth.Secret), map[string]any{"ok": true})
	} else {
		s.configured(ctx, h.ID, "remove", "T4", nil, h.Kind, "harness_subscription", nil, map[string]any{"ok": true})
	}
	return map[string]any{"ok": true, "removed_models": removed}, nil
}

func (s *Server) providerEnable(ctx context.Context, _ *conn, _ *jrpc2.Request, raw json.RawMessage) (any, error) {
	var p struct {
		ProviderID       string `json:"provider_id"`
		Enabled          bool   `json:"enabled"`
		AcknowledgeTerms bool   `json:"acknowledge_terms"`
	}
	_ = json.Unmarshal(raw, &p)
	s.catMu.Lock()
	defer s.catMu.Unlock()
	next := *s.d.Catalog
	next.Providers = append([]config.Provider{}, next.Providers...)
	next.Harnesses = append([]config.Harness{}, next.Harnesses...)
	action := map[bool]string{true: "enable", false: "disable"}[p.Enabled]
	for i := range next.Harnesses {
		h := &next.Harnesses[i]
		if h.ID != p.ProviderID {
			continue
		}
		if p.Enabled && h.VendorTerms == "prohibited" {
			return nil, Fail(CodeVendorTerms, "prohibited", "the vendor's terms prohibit this harness (INV-7)")
		}
		if p.Enabled && h.VendorTerms == "personal_use_only" && s.d.Mode == "shared" {
			return nil, Fail(CodeVendorTerms, "harness_locked_shared_mode", "personal-mode harnesses are locked in shared mode")
		}
		if p.Enabled && (h.VendorTerms == "tolerated" || h.VendorTerms == "personal_use_only") && !p.AcknowledgeTerms {
			return nil, Fail(CodeConfirmationRequired, "acknowledge_terms", "enabling "+h.ID+" requires acknowledging its vendor terms ("+h.VendorTerms+")")
		}
		h.Enabled = p.Enabled
		if err := next.Save(s.d.CatalogPath); err != nil {
			return nil, Fail(CodeInternal, "catalog", err.Error())
		}
		*s.d.Catalog = next
		s.configured(ctx, h.ID, action, "T4", nil, h.Kind, "harness_subscription", nil, map[string]any{"ok": true})
		return map[string]any{"provider_id": h.ID, "enabled": h.Enabled, "vendor_terms": h.VendorTerms, "status": s.harnessView(*h)["status"]}, nil
	}
	for i := range next.Providers {
		pr := &next.Providers[i]
		if pr.ID != p.ProviderID {
			continue
		}
		en := p.Enabled
		pr.Enabled = &en
		if err := next.Save(s.d.CatalogPath); err != nil {
			return nil, Fail(CodeInternal, "catalog", err.Error())
		}
		*s.d.Catalog = next
		s.configured(ctx, pr.ID, action, pr.Tier, pr.Protocol, nil, pr.Auth.Mode, nullable(pr.Auth.Secret), map[string]any{"ok": true})
		return map[string]any{"provider_id": pr.ID, "enabled": en, "vendor_terms": nil, "status": s.providerView(ctx, *pr)["status"]}, nil
	}
	return nil, Fail(CodeNotFound, "provider", "unknown provider "+p.ProviderID)
}

// runTest runs the capability probe of one provider (A11 §9).
func (s *Server) runTest(ctx context.Context, id string) (map[string]any, error) {
	s.catMu.Lock()
	cfg, err := s.d.Catalog.ProviderConfig(id)
	prov := s.d.Catalog.Provider(id)
	var pv config.Provider
	if prov != nil {
		pv = *prov
	}
	s.catMu.Unlock()
	if err != nil {
		return nil, err
	}
	p, err := s.d.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	res, err := p.Probe(ctx)
	if err != nil {
		return nil, err
	}
	// The daemon persists the probe cache (A11 §9.1), whatever the adapter does.
	if res.ProbedAt.IsZero() {
		res.ProbedAt = s.d.Clock().UTC()
	}
	res.ProviderID = id
	_ = s.d.Cache.Store(res)
	models := []map[string]any{}
	for _, m := range res.Models {
		eff := m.MaxContext
		for _, v := range []*int{m.MaxContextMeta, m.MaxContextEffective} {
			if v != nil && *v > 0 && (eff == 0 || *v < eff) {
				eff = *v
			}
		}
		models = append(models, map[string]any{"model_id": m.ModelID, "tool_calling": m.ToolCalling, "structured_output": m.StructuredOutput,
			"streaming": m.Streaming, "max_context": eff})
	}
	var perr any
	var code any
	if res.Error != nil {
		msg := res.Error.Message
		if out, _ := s.d.Broker.Redactor().Redact(msg); out != "" {
			msg = out
		}
		perr = map[string]any{"code": string(res.Error.Code), "message": msg}
		code = string(res.Error.Code)
	}
	out := map[string]any{"ok": res.OK, "latency_ms": res.LatencyMS, "models": models, "error": perr}
	s.configured(ctx, id, "test", pv.Tier, pv.Protocol, nil, pv.Auth.Mode, nullable(pv.Auth.Secret),
		map[string]any{"ok": res.OK, "error_code": code, "latency_ms": res.LatencyMS, "models": models})
	return out, nil
}

// providerTest is provider.test: per-step timeouts from A11 §9.2, capped
// at 300 s for the call (CONFLICTS C-50).
func (s *Server) providerTest(ctx context.Context, _ *conn, _ *jrpc2.Request, raw json.RawMessage) (any, error) {
	var p struct {
		ProviderID string `json:"provider_id"`
	}
	_ = json.Unmarshal(raw, &p)
	s.catMu.Lock()
	known := s.d.Catalog.Provider(p.ProviderID) != nil
	harness := s.d.Catalog.Harness(p.ProviderID) != nil
	s.catMu.Unlock()
	if harness {
		return nil, Fail(CodeUnsupportedInPoC, "harness_probe", "harness probing lands with the harness adapters (M5)")
	}
	if !known {
		return nil, Fail(CodeNotFound, "provider", "unknown provider "+p.ProviderID)
	}
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	res, err := s.runTest(ctx, p.ProviderID)
	if err != nil {
		return nil, Fail(CodeInternal, "probe", err.Error())
	}
	return res, nil
}

// admitted is the PoC admission matrix (WRD-16 §6.3, INV-G): confidential
// data goes only to T0, T1 and T2.
func admitted(classification string) []string {
	if classification == "confidential" {
		return []string{"T0", "T1", "T2"}
	}
	return []string{"T0", "T1", "T2", "T3", "T4"}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// providerModels is provider.models: the catalog with admission per
// classification. Full routing (ranking, health, budgets) lands in M4.
func (s *Server) providerModels(ctx context.Context, _ *conn, _ *jrpc2.Request, raw json.RawMessage) (any, error) {
	var p struct {
		SessionID      string `json:"session_id"`
		Classification string `json:"classification"`
		TaskClass      string `json:"task_class"`
	}
	_ = json.Unmarshal(raw, &p)
	if p.SessionID != "" {
		return nil, Fail(CodeNotFound, "session", "sessions land in M3; pass classification instead")
	}
	if p.TaskClass == "" {
		p.TaskClass = "implement"
	}
	var tiers []string
	if p.Classification != "" {
		tiers = admitted(p.Classification)
	}
	s.catMu.Lock()
	defer s.catMu.Unlock()
	views := []any{}
	for _, m := range s.d.Catalog.Models {
		prov := s.d.Catalog.Provider(m.Provider)
		_, cached := s.lastTest(prov.ID)
		var probe *model.ModelProbe
		if cached != nil {
			for i := range cached.Models {
				if cached.Models[i].ModelID == m.ID {
					probe = &cached.Models[i]
				}
			}
		}
		eff := model.Effective(model.DeclaredCapabilities{ToolCalling: m.Capabilities.ToolCalling, StructuredOutput: m.Capabilities.StructuredOutput,
			Streaming: m.Capabilities.Streaming, MaxContext: m.Capabilities.MaxContext, MaxOutput: m.Capabilities.MaxOutput}, probe)
		caps := map[string]any{"tool_calling": eff.ToolCalling, "structured_output": eff.StructuredOutput, "streaming": eff.Streaming, "max_context": eff.MaxContext}
		if eff.MaxOutput > 0 {
			caps["max_output"] = eff.MaxOutput
		}
		var admissible, code, reason any
		switch {
		case !prov.IsEnabled():
			admissible, code, reason = false, "provider_unconfigured", "provider "+prov.ID+" is disabled"
		case prov.Auth.Secret != "" && !s.d.Broker.Has(ctx, prov.Auth.Secret):
			admissible, code, reason = false, "credential_missing", "no credential stored for "+prov.ID
		case tiers != nil && !contains(tiers, prov.Tier):
			admissible, code, reason = false, "tier_not_admitted", p.Classification+" data is admitted only to "+joinTiers(tiers)+"; "+m.ID+" is "+prov.Tier
		case tiers != nil:
			admissible = true
		}
		var prior any
		if len(m.QualityPrior) == 4 {
			prior = m.QualityPrior
		}
		var pricing any
		if m.Pricing != nil {
			pricing = m.Pricing
		}
		views = append(views, map[string]any{"model_id": m.ID, "provider_id": prov.ID, "kind": "model", "tier": prov.Tier, "admissible": admissible,
			"reason_code": code, "reason": reason, "capabilities": caps, "pricing": pricing, "quality_prior": prior, "pinned": false})
	}
	for _, h := range s.d.Catalog.Harnesses {
		var admissible, code, reason any
		switch {
		case !h.Enabled:
			admissible, code, reason = false, "harness_disabled", h.ID+" is not enabled"
		case tiers != nil && !contains(tiers, "T4"):
			admissible, code, reason = false, "tier_not_admitted", p.Classification+" data is admitted only to "+joinTiers(tiers)+"; "+h.ID+" is T4"
		case tiers != nil:
			admissible = true
		}
		views = append(views, map[string]any{"model_id": h.ID, "provider_id": h.ID, "kind": "harness", "tier": "T4", "admissible": admissible,
			"reason_code": code, "reason": reason, "capabilities": nil, "pricing": nil, "quality_prior": nil, "pinned": false})
	}
	var cls any
	if p.Classification != "" {
		cls = p.Classification
	}
	if tiers == nil {
		tiers = []string{}
	}
	return map[string]any{"classification": cls, "task_class": p.TaskClass, "admitted_tiers": tiers, "models": views}, nil
}

func joinTiers(ts []string) string {
	out := ""
	for i, t := range ts {
		if i > 0 {
			out += ", "
		}
		out += t
	}
	return out
}
