package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

func auditCmd() *cobra.Command {
	a := &cobra.Command{Use: "audit", Short: "Export and verify the hash-chained audit trail"}
	var session, out, file string
	var strict bool
	exp := &cobra.Command{Use: "export", Short: "Write a session's audit export (JSON Lines)", RunE: func(cmd *cobra.Command, _ []string) error {
		params := map[string]any{"session_id": session}
		if out != "" {
			abs, err := filepath.Abs(out)
			if err != nil {
				return err
			}
			params["path"] = abs
		}
		var r map[string]any
		if err := do(cmd, "audit.export", params, &r); err != nil {
			return err
		}
		if !asJSON {
			fmt.Printf("wrote %v (%v events, %v artifacts, %v)\n", r["path"], r["events"], r["artifacts"], r["sha256"])
		}
		return nil
	}}
	exp.Flags().StringVar(&session, "session", "", "session id (ses_…)")
	exp.Flags().StringVar(&out, "out", "", "target directory (default ~/.warden/exports)")
	_ = exp.MarkFlagRequired("session")
	a.AddCommand(exp)

	ver := &cobra.Command{Use: "verify", Short: "Verify a session chain or an export file (exit 0 verified, 1 violations)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			params := map[string]any{"strict": strict}
			switch {
			case session != "" && file != "":
				return errors.New("give --session or --file, not both")
			case session != "":
				params["session_id"] = session
			case file != "":
				abs, err := filepath.Abs(file)
				if err != nil {
					return err
				}
				params["file"] = abs
			default:
				return errors.New("give --session <id> or --file <export.jsonl>")
			}
			var r struct {
				OK               bool            `json:"ok"`
				ChainOK          bool            `json:"chain_ok"`
				StrictOK         *bool           `json:"strict_ok"`
				CheckpointOK     bool            `json:"checkpoint_ok"`
				Events           int             `json:"events"`
				ToolCallsChecked int             `json:"tool_calls_checked"`
				Anchored         bool            `json:"anchored"`
				Violations       json.RawMessage `json:"violations"`
			}
			if err := do(cmd, "audit.verify", params, &r); err != nil {
				return err
			}
			if !asJSON {
				fmt.Printf("events %d, chain %s, checkpoints %s", r.Events, okWord(r.ChainOK), okWord(r.CheckpointOK))
				if r.StrictOK != nil {
					fmt.Printf(", strict %s (%d tool calls)", okWord(*r.StrictOK), r.ToolCallsChecked)
				}
				fmt.Printf(", anchored %v\n", r.Anchored)
				var vs []struct {
					Seq      *int64 `json:"seq"`
					Kind     string `json:"kind"`
					Severity string `json:"severity"`
					Detail   string `json:"detail"`
				}
				_ = json.Unmarshal(r.Violations, &vs)
				for _, v := range vs {
					seq := "-"
					if v.Seq != nil {
						seq = fmt.Sprint(*v.Seq)
					}
					fmt.Printf("  %-7s seq %-6s %-30s %s\n", v.Severity, seq, v.Kind, v.Detail)
				}
				fmt.Println(map[bool]string{true: "VERIFIED", false: "NOT VERIFIED"}[r.OK])
			}
			if !r.OK {
				return exitError{1}
			}
			return nil
		}}
	ver.Flags().StringVar(&session, "session", "", "session id (ses_…)")
	ver.Flags().StringVar(&file, "file", "", "export file to verify (another machine's export works too)")
	ver.Flags().BoolVar(&strict, "strict", false, "also require an allow decision before every tool.exec.start (INV-A)")
	a.AddCommand(ver)
	return a
}

func okWord(b bool) string { return map[bool]string{true: "ok", false: "FAILED"}[b] }

func eventsCmd() *cobra.Command {
	var session string
	var after, limit int
	c := &cobra.Command{Use: "events", Short: "Print events of a session, the sys chain (sys) or all (*)", RunE: func(cmd *cobra.Command, _ []string) error {
		var r struct {
			Events []json.RawMessage `json:"events"`
		}
		if err := do(cmd, "event.query", map[string]any{"session_id": session, "after_seq": after, "limit": limit}, &r); err != nil {
			return err
		}
		if !asJSON {
			for _, e := range r.Events {
				var h struct {
					Seq     int64           `json:"seq"`
					TS      string          `json:"ts"`
					Type    string          `json:"type"`
					Chain   string          `json:"chain"`
					Payload json.RawMessage `json:"payload"`
				}
				_ = json.Unmarshal(e, &h)
				p := string(h.Payload)
				if len(p) > 160 {
					p = p[:160] + "…"
				}
				fmt.Printf("%6d %s %-20s %s\n", h.Seq, h.TS, h.Type, p)
			}
		}
		return nil
	}}
	c.Flags().StringVar(&session, "session", "sys", "ses_… | sys | *")
	c.Flags().IntVar(&after, "after", 0, "only events with seq greater than this")
	c.Flags().IntVar(&limit, "limit", 200, "maximum events (1..1000)")
	return c
}
