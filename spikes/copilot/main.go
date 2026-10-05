// Command copilot is the M1 Copilot SDK spike (see README.md): it starts the
// Copilot runtime over stdio in ModeEmpty with one custom tool and a
// permission handler that rejects everything else. Throwaway; never used by
// the product, which runs the runtime inside a sandbox via warden-exec (M5).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
)

const toolName = "warden_echo"

type echoArgs struct {
	Text string `json:"text" jsonschema:"text to echo back"`
}

func val[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func main() {
	cli := flag.String("cli", "", "engine executable: copilot runtime, or the warden-exec relay shim (option C)")
	home := flag.String("home", "", "COPILOT_HOME (ModeEmpty requires it); login file or token lives here")
	cwd := flag.String("cwd", "", "session working directory (empty scratch dir, Q6)")
	model := flag.String("model", "", "model id; empty = runtime default")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // SendAndWait defaults to 60s without a deadline
	defer cancel()

	client := copilot.NewClient(&copilot.ClientOptions{
		Connection: copilot.StdioConnection{ // never nil: nil lets COPILOT_SDK_DEFAULT_CONNECTION pick in-process
			Path: *cli,                                                                 // SDK appends: --headless --no-auto-update --log-level error --stdio
			Env:  []string{"PATH=" + os.Getenv("PATH"), "HOME=" + *home, "NO_COLOR=1"}, // explicit; nil would leak os.Environ()
		},
		Mode:          copilot.ModeEmpty, // no built-ins, no env context, no telemetry, no keytar
		BaseDirectory: *home,             // -> COPILOT_HOME
		LogLevel:      "error",
	})
	if err := client.Start(ctx); err != nil { // spawn + connect handshake (protocol v3)
		log.Fatalf("start: %v", err)
	}
	defer func() {
		if err := client.Stop(); err != nil {
			log.Printf("stop: %v", err)
		}
	}()

	echo := copilot.DefineTool(toolName, "Echo text back. This is the only tool you have.",
		func(a echoArgs, inv copilot.ToolInvocation) (string, error) {
			log.Printf("TOOL %s call=%s text=%q", inv.ToolName, inv.ToolCallID, a.Text) // Q2
			return "echo: " + a.Text, nil
		})

	session, err := client.CreateSession(ctx, &copilot.SessionConfig{
		Model:            *model,
		WorkingDirectory: *cwd,
		Streaming:        copilot.Bool(true),
		Tools:            []copilot.Tool{echo},
		AvailableTools:   copilot.NewToolSet().AddCustom(toolName).ToSlice(), // Q3: allowlist = our tool only
		SystemMessage: &copilot.SystemMessageConfig{Mode: "replace",
			Content: "You are a test agent. You can act only through the warden_echo tool."},
		OnPermissionRequest: func(req copilot.PermissionRequest, inv copilot.PermissionInvocation) (rpc.PermissionDecision, error) {
			switch r := req.(type) {
			case *copilot.PermissionRequestCustomTool:
				if r.ToolName == toolName { // our own tools also ask; approve them
					log.Printf("PERMISSION custom-tool %s ALLOW call=%v", r.ToolName, val(r.ToolCallID))
					return &rpc.PermissionDecisionApproveOnce{}, nil
				}
			case *copilot.PermissionRequestShell:
				log.Printf("PERMISSION shell DENY %q", r.FullCommandText)
			case *copilot.PermissionRequestWrite:
				log.Printf("PERMISSION write DENY %s", r.FileName)
			default:
				log.Printf("PERMISSION %s DENY", req.Kind())
			}
			fb := "denied by Warden policy: only runtime tools may be used" // reaches the model (Q5)
			return &rpc.PermissionDecisionReject{Feedback: &fb}, nil
		},
		Hooks: &copilot.SessionHooks{
			OnPreToolUse: func(in copilot.PreToolUseHookInput, _ copilot.HookInvocation) (*copilot.PreToolUseHookOutput, error) {
				log.Printf("HOOK preToolUse %s", in.ToolName) // Q4: ordering vs permission and handler
				if in.ToolName == toolName {
					return &copilot.PreToolUseHookOutput{}, nil // no opinion; permission handler decides
				}
				return &copilot.PreToolUseHookOutput{PermissionDecision: "deny", PermissionDecisionReason: "not a runtime tool"}, nil
			},
			OnPostToolUse: func(in copilot.PostToolUseHookInput, _ copilot.HookInvocation) (*copilot.PostToolUseHookOutput, error) {
				log.Printf("HOOK postToolUse %s", in.ToolName)
				return nil, nil
			},
		},
		OnEvent: func(ev copilot.SessionEvent) {
			switch d := ev.Data.(type) {
			case *copilot.AssistantMessageDeltaData:
				fmt.Print(d.DeltaContent)
			case *copilot.AssistantMessageData:
				fmt.Printf("\n[assistant.message] %d chars\n", len(d.Content))
			case *copilot.ToolExecutionStartData:
				log.Printf("[tool.execution_start] %s %s", d.ToolName, d.ToolCallID)
			case *copilot.ToolExecutionCompleteData:
				log.Printf("[tool.execution_complete] %s success=%v", d.ToolCallID, d.Success)
			case *copilot.AssistantUsageData: // Q7
				log.Printf("[assistant.usage] model=%s in=%v out=%v cost=%v", d.Model, val(d.InputTokens), val(d.OutputTokens), val(d.Cost))
			case *copilot.SessionShutdownData:
				log.Printf("[session.shutdown] premiumRequests=%v", val(d.TotalPremiumRequests))
			case *copilot.SessionErrorData:
				log.Printf("[session.error] %s: %s", d.ErrorType, d.Message)
			default:
				log.Printf("[%s]", ev.Type())
			}
		},
	})
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer session.Disconnect()

	reply, err := session.SendAndWait(ctx, copilot.MessageOptions{
		Prompt: "Call warden_echo with text 'hello from warden'. Then run the shell command `ls /` and report what happened.",
	})
	if err != nil {
		log.Fatalf("send: %v", err)
	}
	if reply == nil { // idle without any root assistant.message
		log.Fatal("no final assistant message")
	}
	if d, ok := reply.Data.(*copilot.AssistantMessageData); ok {
		fmt.Printf("\nFINAL: %s\n", d.Content)
	}
}
