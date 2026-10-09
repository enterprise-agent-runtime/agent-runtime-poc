package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"warden.dev/warden/internal/platform"
)

// preset is a known provider shape (WRD-16 §6.1).
type preset struct {
	protocol, baseURL, tier, auth, header string
	models                                []map[string]any
}

func priors(v float64) map[string]any {
	return map[string]any{"plan": v, "implement": v, "verify": v, "summarize": v}
}

var presets = map[string]preset{
	"anthropic": {protocol: "anthropic-messages", baseURL: "https://api.anthropic.com", tier: "T3", auth: "api_key",
		models: []map[string]any{{"id": "anthropic/claude-sonnet", "provider": "anthropic", "model": "claude-sonnet-5-5",
			"capabilities": map[string]any{"tool_calling": "native", "structured_output": true, "streaming": true, "max_context": 200000, "max_output": 64000},
			"pricing":      map[string]any{"input_per_mtok": 3.00, "output_per_mtok": 15.00, "currency": "USD"}, "quality_prior": priors(0.9)}}},
	"ollama":       {protocol: "openai-compatible", baseURL: "http://127.0.0.1:11434/v1", tier: "T0", auth: "none"},
	"lmstudio":     {protocol: "openai-compatible", baseURL: "http://127.0.0.1:1234/v1", tier: "T0", auth: "none"},
	"openai":       {protocol: "openai-compatible", baseURL: "https://api.openai.com/v1", tier: "T3", auth: "api_key"},
	"azure-openai": {protocol: "openai-compatible", tier: "T2", auth: "api_key", header: "api-key"},
	"company-vllm": {protocol: "openai-compatible", tier: "T1", auth: "bearer"},
}

func providerCmd() *cobra.Command {
	p := &cobra.Command{Use: "provider", Short: "Configure model providers (secrets go to the OS keychain)"}
	var baseURL, tier, auth, secretEnv, cert, key string
	var askKey, askToken, azure, noTest bool
	var models []string
	add := &cobra.Command{Use: "add <id>", Args: cobra.ExactArgs(1),
		Short: "Add or update a provider: anthropic, ollama, lmstudio, openai, azure-openai, company-vllm, or any id with --base-url/--tier/--auth",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			pr := presets[id]
			set := func(dst *string, v string) {
				if v != "" {
					*dst = v
				}
			}
			set(&pr.baseURL, baseURL)
			set(&pr.tier, tier)
			set(&pr.auth, auth)
			if pr.protocol == "" {
				pr.protocol = "openai-compatible"
			}
			if azure {
				pr.header = "api-key"
			}
			if pr.baseURL == "" || pr.tier == "" || pr.auth == "" {
				return errors.New("unknown provider id: give --base-url, --tier (T0..T3) and --auth (none|api_key|bearer|mtls)")
			}
			authSpec := map[string]any{}
			switch pr.auth {
			case "none", "api_key":
				authSpec["mode"] = pr.auth
			case "bearer", "mtls":
				authSpec["mode"], authSpec["kind"] = "gateway", pr.auth
			default:
				return fmt.Errorf("--auth %q: use none, api_key, bearer or mtls", pr.auth)
			}
			if pr.header != "" {
				authSpec["header"] = pr.header
			}
			spec := map[string]any{"id": id, "protocol": pr.protocol, "base_url": pr.baseURL, "tier": pr.tier, "auth": authSpec}
			ms := pr.models
			for _, m := range models {
				catID, wire, ok := strings.Cut(m, "=")
				if !ok {
					catID, wire = id+"/"+m, m
				}
				ms = append(ms, map[string]any{"id": catID, "provider": id, "model": wire,
					"capabilities": map[string]any{"tool_calling": "native", "structured_output": true, "streaming": true, "max_context": 32768},
					"pricing":      nil, "quality_prior": priors(0.5)})
			}
			if len(models) > 0 {
				ms = ms[len(pr.models):]
			}
			if len(ms) > 0 {
				spec["models"] = ms
			}
			params := map[string]any{"spec": spec, "confirm": true, "test": !noTest}
			// The secret is read here, at setup time, and handed to the
			// daemon once; it is stored only in the keychain (CLAUDE.md §3).
			var value []byte
			switch {
			case secretEnv != "":
				value = []byte(os.Getenv(secretEnv))
				if len(value) == 0 {
					return fmt.Errorf("environment variable %s is empty", secretEnv)
				}
			case askKey || askToken:
				fmt.Fprintf(os.Stderr, "%s for %s (not echoed): ", map[bool]string{true: "API key", false: "Token"}[askKey], id)
				v, err := platform.ReadSecretLine(os.Stdin)
				if err != nil {
					return err
				}
				value = v
			}
			if len(value) > 0 {
				params["secret"] = map[string]string{"value": string(value)}
			}
			if pr.auth == "mtls" {
				if cert == "" || key == "" {
					return errors.New("mtls needs --cert and --key (the key is copied into the keychain; its path is not stored)")
				}
				params["secret_files"] = map[string]string{"cert": cert, "key": key}
			}
			var r struct {
				ProviderID string         `json:"provider_id"`
				Test       map[string]any `json:"test"`
			}
			if err := do(cmd, "provider.add", params, &r); err != nil {
				return err
			}
			if !asJSON {
				fmt.Printf("added %s (%s, %s)\n", r.ProviderID, pr.protocol, pr.tier)
				printTest(r.Test)
			}
			return nil
		}}
	f := add.Flags()
	f.StringVar(&baseURL, "base-url", "", "endpoint base URL (openai-compatible: ends in /v1)")
	f.StringVar(&tier, "tier", "", "trust tier T0 (loopback), T1 (company-hosted), T2 (company tenant), T3 (vendor API)")
	f.StringVar(&auth, "auth", "", "none | api_key | bearer | mtls")
	f.BoolVar(&askKey, "api-key", false, "prompt for an API key (stored in the keychain)")
	f.BoolVar(&askToken, "token", false, "prompt for a gateway bearer token (stored in the keychain)")
	f.StringVar(&secretEnv, "secret-env", "", "read the key or token from this environment variable at setup time only")
	f.BoolVar(&azure, "azure", false, "send the key in the api-key header (Azure OpenAI)")
	f.StringVar(&cert, "cert", "", "mtls: client certificate chain (PEM)")
	f.StringVar(&key, "key", "", "mtls: unencrypted private key (PEM); copied into the keychain")
	f.StringArrayVar(&models, "model", nil, "model to add: <wire-name> or <catalog-id>=<wire-name> (repeatable); local servers are discovered")
	f.BoolVar(&noTest, "no-test", false, "skip the capability probe")
	p.AddCommand(add)

	p.AddCommand(&cobra.Command{Use: "test <id>", Args: cobra.ExactArgs(1), Short: "Probe connectivity and capabilities (provider.test)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var r map[string]any
			if err := do(cmd, "provider.test", map[string]any{"provider_id": args[0]}, &r); err != nil {
				return err
			}
			if !asJSON {
				printTest(r)
			}
			if r["ok"] != true {
				return exitError{1}
			}
			return nil
		}})
	p.AddCommand(&cobra.Command{Use: "list", Short: "List providers and harnesses", RunE: func(cmd *cobra.Command, _ []string) error {
		var r struct {
			Providers []map[string]any `json:"providers"`
			Harnesses []map[string]any `json:"harnesses"`
		}
		if err := do(cmd, "provider.list", nil, &r); err != nil {
			return err
		}
		if !asJSON {
			if len(r.Providers)+len(r.Harnesses) == 0 {
				fmt.Println("no providers configured; try: warden provider add anthropic --api-key, or warden provider add ollama")
			}
			for _, p := range r.Providers {
				fmt.Printf("%-14s %-19s %-3v %-9v %-18v %v\n", p["provider_id"], p["protocol"], p["tier"], p["auth_mode"], p["status"], p["models"])
			}
			for _, h := range r.Harnesses {
				fmt.Printf("%-14s %-19s T4  %-9v %-18v vendor_terms=%v\n", h["harness_id"], h["kind"], h["billing"], h["status"], h["vendor_terms"])
			}
		}
		return nil
	}})
	p.AddCommand(&cobra.Command{Use: "remove <id>", Args: cobra.ExactArgs(1), Short: "Remove a provider and its keychain items",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := do(cmd, "provider.remove", map[string]any{"provider_id": args[0], "confirm": true}, nil); err != nil {
				return err
			}
			if !asJSON {
				fmt.Println("removed", args[0])
			}
			return nil
		}})
	var disable bool
	en := &cobra.Command{Use: "enable <id>", Args: cobra.ExactArgs(1), Short: "Enable (or --disable) a provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			return do(cmd, "provider.enable", map[string]any{"provider_id": args[0], "enabled": !disable}, nil)
		}}
	en.Flags().BoolVar(&disable, "disable", false, "disable instead")
	p.AddCommand(en)
	return p
}

func printTest(r map[string]any) {
	if r == nil {
		return
	}
	if r["ok"] == true {
		fmt.Printf("probe ok (%v ms)\n", r["latency_ms"])
	} else if e, ok := r["error"].(map[string]any); ok {
		fmt.Printf("probe failed: %v: %v\n", e["code"], e["message"])
	}
	if ms, ok := r["models"].([]any); ok {
		for _, m := range ms {
			mm := m.(map[string]any)
			fmt.Printf("  %-32v tool_calling=%-8v structured=%-5v streaming=%-5v max_context=%v\n", mm["model_id"], mm["tool_calling"], mm["structured_output"], mm["streaming"], mm["max_context"])
		}
	}
}

func harnessCmd() *cobra.Command {
	h := &cobra.Command{Use: "harness", Short: "Enable subscription harnesses (Copilot SDK; Codex and Claude Code optional)"}
	var ack, disable bool
	en := &cobra.Command{Use: "enable <id>", Args: cobra.ExactArgs(1), Short: "Enable a harness listed in models.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			var r map[string]any
			if err := do(cmd, "provider.enable", map[string]any{"provider_id": args[0], "enabled": !disable, "acknowledge_terms": ack}, &r); err != nil {
				return err
			}
			if !asJSON {
				fmt.Printf("%s enabled=%v vendor_terms=%v\n", args[0], r["enabled"], r["vendor_terms"])
			}
			return nil
		}}
	en.Flags().BoolVar(&ack, "acknowledge-terms", false, "acknowledge the vendor terms (tolerated, personal_use_only)")
	en.Flags().BoolVar(&disable, "disable", false, "disable instead")
	h.AddCommand(en)
	return h
}

func modelsCmd() *cobra.Command {
	var classification string
	c := &cobra.Command{Use: "models", Short: "List catalog models and their admission for a classification", RunE: func(cmd *cobra.Command, _ []string) error {
		params := map[string]any{}
		if classification != "" {
			params["classification"] = classification
		}
		var r struct {
			AdmittedTiers []string         `json:"admitted_tiers"`
			Models        []map[string]any `json:"models"`
		}
		if err := do(cmd, "provider.models", params, &r); err != nil {
			return err
		}
		if !asJSON {
			if classification != "" {
				fmt.Printf("classification %s admits %v\n", classification, r.AdmittedTiers)
			}
			for _, m := range r.Models {
				adm := "-"
				if a, ok := m["admissible"].(bool); ok {
					adm = map[bool]string{true: "admitted", false: "NOT ADMITTED"}[a]
				}
				reason := ""
				if m["reason"] != nil {
					reason = fmt.Sprint(m["reason"])
				}
				fmt.Printf("%-34v %-7v %-3v %-13s %s\n", m["model_id"], m["kind"], m["tier"], adm, reason)
			}
		}
		return nil
	}}
	c.Flags().StringVar(&classification, "classification", "", "public | internal | confidential")
	return c
}
