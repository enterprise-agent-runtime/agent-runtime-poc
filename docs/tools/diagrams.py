import subprocess, os, textwrap
OUT = "../markdown/img"
os.makedirs(OUT, exist_ok=True)

BASE = 'graph [fontname="Helvetica", fontsize=11, dpi=160, pad=0.2, nodesep=0.35, ranksep=0.45, bgcolor="white"]; node [fontname="Helvetica", fontsize=10, shape=box, style="rounded,filled", fillcolor="#F4F7FB", color="#5B7089", penwidth=1.2]; edge [fontname="Helvetica", fontsize=9, color="#3D4F63", arrowsize=0.8];'

D = {}

D["architecture_layers"] = r'''
digraph G { rankdir=TB; %s
  subgraph cluster_exp { label="Experience layer (clients — no agent logic)"; style="rounded,filled"; fillcolor="#EEF3F9"; color="#9DB0C4";
    ui [label="Desktop UI\n(Tauri 2 + React/TS)"]; cli [label="CLI\n(warden)"]; ci [label="CI / headless\n(warden run --non-interactive)"]; api [label="Remote API\n(later: workers, control plane)"]; }
  rt_api [label="Runtime API (JSON-RPC 2.0 over local socket / named pipe)", fillcolor="#DCE6F2", width=6.4];
  subgraph cluster_kernel { label="Agent Runtime kernel (wardend) — the trust boundary"; style="rounded,filled"; fillcolor="#FFF8E7"; color="#D9A62E";
    sess [label="Session &\nworkflow state"]; orch [label="Orchestrator\n(planner, DAG scheduler,\njoins, retries, repair)"]; loop [label="Agent loop\n(context, model call,\nproposal handling)"];
    pdp [label="Policy Decision Point\n(capabilities, rules,\napprovals, obligations)", fillcolor="#FDE9D9", color="#C0504D"]; router [label="Model Router\n(classification tiers,\ncapabilities, fallback)"]; ctx [label="Context manager\n(selection, provenance,\ncompaction, redaction)"];
    audit [label="Event & artifact store\n(SQLite, hash chain)"]; secrets [label="Secrets broker\n(OS keychain, refs only)"]; }
  subgraph cluster_ports { label="Ports (adapters behind stable interfaces)"; style="rounded,filled"; fillcolor="#EEF3F9"; color="#9DB0C4";
    prov [label="Model provider adapters\nanthropic · openai · gemini\nbedrock · vertex · foundry\nopenai-compatible · ollama"]; harn [label="External harness adapters\nCopilot SDK · Codex SDK\nClaude Code (headless)"];
    exec [label="Tool executors\n(run inside sandbox via\nwarden-exec)"]; proxy [label="Egress proxy\n(allowlist + credential\ninjection)"]; }
  subgraph cluster_sb { label="Sandbox (per task) — no secrets, no direct network"; style="rounded,filled"; fillcolor="#EAF7EA"; color="#5FA65F";
    wt [label="Git worktree\n(rw)"]; tc [label="Toolchain\n(ro)"]; procs [label="Agent-requested processes\n(build, test, lint, MCP stdio)"]; }
  ext [label="Model endpoints / vendor agents / allowlisted hosts (git remote, package registries)", fillcolor="#F0F0F0", width=6.4];
  ui -> rt_api; cli -> rt_api; ci -> rt_api; api -> rt_api;
  rt_api -> sess; sess -> orch; orch -> loop; loop -> pdp; loop -> router; loop -> ctx; pdp -> audit; loop -> audit; router -> prov; router -> harn; pdp -> exec; secrets -> prov; secrets -> proxy;
  exec -> wt; exec -> procs; procs -> proxy [label="only via proxy"]; prov -> ext; harn -> procs; proxy -> ext; tc -> procs [style=dashed, arrowhead=none];
}''' % BASE

D["process_topology"] = r'''
digraph G { rankdir=LR; %s
  subgraph cluster_host { label="Developer workstation (host)"; style="rounded,filled"; fillcolor="#F9FAFC"; color="#9DB0C4";
    ui [label="Desktop shell\n(Tauri 2 webview,\nReact UI)"]; cli [label="warden CLI"];
    d [label="wardend\n(runtime daemon, Go)\nsession · orchestrator · PDP\nrouter · executors · proxy", fillcolor="#FFF8E7", color="#D9A62E"];
    kc [label="OS keychain\n(macOS Keychain /\nWindows Credential Mgr /\nlibsecret)"]; db [label="~/.warden/\nSQLite + artifacts\n+ worktrees"];
    subgraph cluster_sb { label="Sandbox L1/L2 (per task)"; style="rounded,filled"; fillcolor="#EAF7EA"; color="#5FA65F";
      ex [label="warden-exec\n(tool executor,\nJSON-RPC over pipe)"]; p [label="child processes\n(npm test, go build,\nsemgrep, MCP servers)"]; } }
  m [label="Model endpoints\n(API key / IAM / local)", fillcolor="#F0F0F0"]; g [label="Allowlisted hosts\n(git remote, npm, PyPI)", fillcolor="#F0F0F0"];
  ui -> d [label="JSON-RPC\n(unix socket / named pipe,\nper-session token)"]; cli -> d [label="JSON-RPC"]; d -> kc [label="read secret refs"]; d -> db;
  d -> ex [label="tool commands"]; ex -> p; p -> d [label="egress proxy\n(CONNECT/SOCKS5)", style=dashed]; d -> m [label="TLS, credentials\ninjected host-side"]; d -> g [label="proxied, allowlisted,\ncredential injection"];
}''' % BASE

D["trust_boundaries"] = r'''
digraph G { rankdir=LR; %s
  subgraph cluster_t0 { label="Untrusted inputs"; style="rounded,filled"; fillcolor="#FDE9D9"; color="#C0504D";
    repo [label="Repository content\n(files, hooks, configs)"]; model [label="Model output\n(proposals, text)"]; tools [label="Tool results,\nMCP servers, web"]; pkg [label="Agent packages\n(3rd-party)"]; uiu [label="UI / CLI requests"]; }
  subgraph cluster_t1 { label="Trusted computing base"; style="rounded,filled"; fillcolor="#FFF8E7"; color="#D9A62E";
    rt [label="wardend runtime\n(PDP, executors, proxy,\nsecrets broker, audit)"]; pol [label="Policy bundles\n(user / org, signed)"]; }
  subgraph cluster_t2 { label="Confined execution"; style="rounded,filled"; fillcolor="#EAF7EA"; color="#5FA65F"; sb [label="Sandbox\n(worktree rw, no secrets,\nproxy-only network)"]; }
  ext [label="External services\n(models, git hosts, registries)", fillcolor="#F0F0F0"];
  repo -> sb [label="mounted"]; model -> rt [label="proposal only"]; tools -> rt [label="tagged untrusted"]; pkg -> rt [label="verified, manifest-scoped"]; uiu -> rt [label="authenticated API"];
  pol -> rt; rt -> sb [label="grants capabilities,\nspawns executor"]; sb -> ext [label="only via proxy,\nallowlist"]; rt -> ext [label="model calls,\ncredentials"];
}''' % BASE

D["agent_loop"] = r'''
digraph G { rankdir=TB; %s node [shape=box];
  start [label="Task started\n(worktree + sandbox ready)", shape=ellipse, fillcolor="#DCE6F2"];
  ctx [label="Build context\nmanifest instructions + task input\n+ input artifacts + workspace summary\n+ tagged tool results"];
  budget [label="Budget check\nsteps ≤ max_steps · tokens ≤ max_tokens\nwall-clock ≤ timeout", shape=diamond, fillcolor="#FFF8E7"];
  route [label="Model Router selects provider/model\n(classification tier, capabilities, fallback)"];
  call [label="Canonical Model API call\n(host-side, streaming, tools = granted only)"];
  kind [label="Response kind?", shape=diamond, fillcolor="#FFF8E7"];
  final [label="Validate output against output schema\n→ produce artifacts → task succeeded", fillcolor="#EAF7EA"];
  pdp [label="Policy Decision Point\nevaluate(action, capability grants, rules, taint)"];
  dec [label="Decision", shape=diamond, fillcolor="#FFF8E7"];
  appr [label="Approval request\n(UI/CLI; scope once/task/session/workspace)\ntask → waiting_for_approval"];
  exe [label="Execute tool in sandbox (warden-exec)\nobligations applied (timeouts, redaction)"];
  obs [label="Observe result\ntag as untrusted content · secret-scan/redact\n· update taint · emit events"];
  deny [label="Denied → structured reason\nreturned to model as tool error"];
  compact [label="Context compaction\n(summarize older turns, keep artifacts refs)"];
  fail [label="Budget exhausted →\ntask failed (budget) or checkpoint + escalate", fillcolor="#FDE9D9"];
  start -> ctx -> budget; budget -> route [label="ok"]; budget -> fail [label="exceeded"]; route -> call -> kind;
  kind -> final [label="final answer"]; kind -> pdp [label="tool proposal(s)"]; kind -> compact [label="context_too_long"]; compact -> ctx;
  pdp -> dec; dec -> exe [label="allow (+obligations)"]; dec -> appr [label="approval_required"]; dec -> deny [label="deny"];
  appr -> exe [label="approved"]; appr -> deny [label="rejected"]; exe -> obs; deny -> obs; obs -> ctx;
}''' % BASE

D["model_stack"] = r'''
digraph G { rankdir=TB; %s
  a [label="Agent (manifest: required capabilities, allow/deny lists)"];
  api [label="Canonical Model API\nModelRequest → stream of events (text, tool_use, reasoning-opaque, usage, error)", fillcolor="#DCE6F2", width=6.2];
  r [label="Model Router\ninputs: task class · data classification · required capabilities · cost/latency budget · availability\noutput: (provider, model) + routing_decision event", fillcolor="#FFF8E7", width=6.2];
  subgraph cluster_ax { label="Provider definition = protocol × auth mode × hosting/trust tier"; style="rounded,filled"; fillcolor="#EEF3F9"; color="#9DB0C4";
    p1 [label="Protocol adapters\nanthropic-messages · openai-chat\nopenai-responses · gemini\nbedrock-converse · ollama-native"];
    p2 [label="Auth modes\napi_key · cloud_iam (Entra/SigV4/ADC)\ngateway (SSO/mTLS) · none (local)\nharness_subscription (vendor SDK)"];
    p3 [label="Hosting / trust tier\nT0 local · T1 private-hosted\nT2 enterprise cloud · T3 vendor API\nT4 consumer subscription harness"]; }
  e1 [label="Anthropic API", fillcolor="#F0F0F0"]; e2 [label="OpenAI / Azure Foundry", fillcolor="#F0F0F0"]; e3 [label="Bedrock / Vertex", fillcolor="#F0F0F0"]; e4 [label="vLLM / Ollama / llama.cpp\n/ LM Studio (OpenAI-compatible)", fillcolor="#F0F0F0"]; e5 [label="Enterprise LLM gateway", fillcolor="#F0F0F0"]; e6 [label="Copilot SDK / Codex SDK\n(vendor agent engines)", fillcolor="#F0F0F0"];
  a -> api -> r; r -> p1; r -> p2; r -> p3; p1 -> e1; p1 -> e2; p1 -> e3; p1 -> e4; p1 -> e5; p2 -> e6 [label="external harness\nadapter kind"];
}''' % BASE

D["orchestration_dag"] = r'''
digraph G { rankdir=TB; %s
  req [label="Request: add OAuth login (Google, GitHub)\nclassified: high complexity, confidential data", fillcolor="#DCE6F2"];
  ex [label="T1 explore\nrepo map, conventions\n(read-only)"];
  sec [label="T2 security analysis\n(read-only)"]; arch [label="T3 architecture analysis\n(read-only)"];
  plan [label="T4 plan → Workflow (DAG)\nartifact: plan.v1", fillcolor="#FFF8E7"];
  gate1 [label="G1 approval gate\nuser reviews plan", shape=hexagon, fillcolor="#FDE9D9"];
  be [label="T5 backend impl\nworktree wt-5"]; fe [label="T6 frontend impl\nworktree wt-6"]; db [label="T7 migration\nworktree wt-7"];
  integ [label="T8 integrate\nmerge wt-5..7 → session branch\nconflicts → integrator agent"];
  build [label="T9 build + lint"]; test [label="T10 tests"]; rev [label="T11 security review\n(read-only, diff)"];
  repair [label="T12 repair (conditional)\nfailure analysis → fix task(s)", style="rounded,filled,dashed"];
  gate2 [label="G2 approval gate\nfinal diff + evidence", shape=hexagon, fillcolor="#FDE9D9"];
  done [label="Deliver: branch/patch, reports,\ncost + audit trace", fillcolor="#EAF7EA"];
  req -> ex; ex -> sec; ex -> arch; sec -> plan; arch -> plan; plan -> gate1; gate1 -> be; gate1 -> fe; gate1 -> db;
  be -> integ; fe -> integ; db -> integ; integ -> build; integ -> test; integ -> rev; build -> repair [style=dashed, label="fail"]; test -> repair [style=dashed, label="fail"]; rev -> repair [style=dashed, label="blocking finding"];
  repair -> integ [style=dashed, label="re-run affected"]; build -> gate2; test -> gate2; rev -> gate2; gate2 -> done;
}''' % BASE

D["task_state_machine"] = r'''
digraph G { rankdir=LR; %s node [shape=box, style="rounded,filled"];
  created -> queued [label="deps satisfied"]; queued -> running [label="scheduled\n(attempt n)"];
  running -> waiting_for_approval [label="approval_required"]; waiting_for_approval -> running [label="approved"]; waiting_for_approval -> failed [label="rejected /\nexpired"];
  running -> waiting_for_input [label="needs user input"]; waiting_for_input -> running [label="input provided"];
  running -> succeeded [label="output validated"]; running -> failed [label="non-retryable error\nor attempts exhausted"];
  running -> queued [label="retryable error\n& attempts < max\n(backoff)"];
  running -> timed_out [label="timeout"]; timed_out -> queued [label="retry if allowed"];
  running -> cancelled [label="cancel"]; queued -> cancelled [label="cancel"]; waiting_for_approval -> cancelled [label="cancel"];
  created -> skipped [label="upstream failed &\non_failure=skip"]; created -> blocked [label="upstream failed &\non_failure=fail"];
  succeeded [fillcolor="#EAF7EA"]; failed [fillcolor="#FDE9D9"]; cancelled [fillcolor="#F0F0F0"]; timed_out [fillcolor="#FDE9D9"]; skipped [fillcolor="#F0F0F0"]; blocked [fillcolor="#F0F0F0"];
}''' % BASE

D["policy_flow"] = r'''
digraph G { rankdir=TB; %s
  in [label="Action request\n{actor: agent@ver, task, tool, operation, resource, args,\ndata classification, taint, environment, prior approvals}", fillcolor="#DCE6F2", width=6.4];
  cap [label="1. Capability check\nIs (tool, operation, resource) inside the task's granted capabilities?\n(manifest ∩ workspace grants ∩ session grants)"];
  plat [label="2. Platform invariants (non-overridable)\nsecret paths, host escape, git hooks/config, destructive ops outside worktree"];
  rules [label="3. Rule sets, most specific first\nplatform defaults → org bundle → user policy → workspace policy (restrict-only) → session grants"];
  merge [label="4. Combine\nany DENY → deny; else any APPROVAL_REQUIRED → approval; else ALLOW if an explicit rule or capability allows\nobligations = union (sandbox_only, redact, quota, restrict_model, timeout)"];
  out [label="Decision {effect, reason, matched_rules[], obligations[], approver, ttl} → event + explanation to UI", fillcolor="#FFF8E7", width=6.4];
  in -> cap; cap -> plat [label="in grants"]; cap -> out [label="not granted → deny"]; plat -> rules [label="pass"]; plat -> out [label="violated → deny"]; rules -> merge -> out;
}''' % BASE

D["data_model"] = r'''
digraph G { rankdir=LR; %s node [shape=record, style="filled"];
  ws [label="{Workspace|id\lroot path\lclassification\lpolicy refs\l}"];
  s [label="{Session|id\lworkspace_id\luser\lcreated_at\lstatus\l}"];
  w [label="{WorkflowRun|id\lsession_id\lspec (DAG)\lstatus\l}"];
  t [label="{Task|id\lworkflow_id\lagent@version\lstate\lattempts\linputs[]\l}"];
  e [label="{Execution (attempt)|id\ltask_id\lsandbox id\lworktree\lmodel/provider\lstarted/ended\l}"];
  ev [label="{Event|seq\lts\ltype\lactor\lpayload (json)\lprev_hash\lhash\l}"];
  ar [label="{Artifact|id\ltype\lschema\lcontent_hash\luri\lprovenance (json)\l}"];
  ap [label="{Approval|id\laction\ldecision\lscope\lapprover\lexpires\l}"];
  ws -> s; s -> w; w -> t; t -> e; e -> ev [label="emits"]; e -> ar [label="produces"]; ar -> ar [label="derived_from"]; e -> ap [label="requests"]; ap -> ev;
}''' % BASE

D["sandbox_levels"] = r'''
digraph G { rankdir=LR; %s
  l0 [label="L0 — none\n(never used for agent-requested\nprocesses; host tools only\nfor explicit host-scoped ops)", fillcolor="#FDE9D9"];
  l1 [label="L1 — OS-native process sandbox\nLinux: bubblewrap + seccomp + user/pid/net ns\nmacOS: Seatbelt (sandbox-exec profile)\n+ egress proxy · MVP default on macOS/Linux", fillcolor="#FFF8E7"];
  l2 [label="L2 — rootless OCI container\nDocker/Podman, read-only image,\nworktree bind mount, no-new-privileges,\ncap-drop ALL, netns → proxy · MVP default on Windows,\noptional everywhere", fillcolor="#EAF7EA"];
  l3 [label="L3 — microVM / hardened\ngVisor · Firecracker · Apple containers\nfor CI/remote workers and untrusted repos\n(Phase 3+)", fillcolor="#DCE6F2"];
  l0 -> l1 -> l2 -> l3 [label="stronger isolation →"];
}''' % BASE

D["deployment_local"] = r'''
digraph G { rankdir=LR; %s
  subgraph cluster_ws { label="MVP: single workstation, local-first"; style="rounded,filled"; fillcolor="#F9FAFC"; color="#9DB0C4";
    ui [label="Desktop app / CLI"]; d [label="wardend", fillcolor="#FFF8E7"]; sb [label="Sandbox L1/L2", fillcolor="#EAF7EA"]; st [label="~/.warden\nconfig · policy · agents · SQLite · artifacts"]; }
  cloud [label="Model providers\n(API key / IAM)", fillcolor="#F0F0F0"]; local [label="Local models\n(Ollama / vLLM on LAN)", fillcolor="#F0F0F0"]; git [label="Git host", fillcolor="#F0F0F0"];
  ui -> d; d -> sb; d -> st; d -> cloud; d -> local; d -> git [label="proxied"];
}''' % BASE

D["deployment_hybrid"] = r'''
digraph G { rankdir=TB; %s
  subgraph cluster_cp { label="Enterprise control plane (Phase 3+), private cloud / on-prem"; style="rounded,filled"; fillcolor="#EEF3F9"; color="#9DB0C4";
    idp [label="IdP (OIDC/SAML)"]; reg [label="Agent registry\n(OCI artifacts, signed)"]; mreg [label="Model & provider registry\n(catalog, tiers, credential refs)"]; pol [label="Policy service\n(signed bundles, TTL)"]; aud [label="Audit ingestion\n(events, artifacts index)"]; ana [label="Usage & cost analytics"]; fleet [label="Worker fleet manager\n(job queue)"]; }
  subgraph cluster_ex { label="Execution plane"; style="rounded,filled"; fillcolor="#FFF8E7"; color="#D9A62E";
    dev [label="Developer workstations\n(wardend + desktop)"]; ci [label="CI runners\n(wardend headless)"]; wk [label="Remote workers\n(wardend server mode, L2/L3 sandboxes)"]; }
  gw [label="Enterprise LLM gateway /\nprivate model serving", fillcolor="#F0F0F0"]; vend [label="Vendor APIs (if allowed by tier)", fillcolor="#F0F0F0"];
  idp -> dev; idp -> wk; reg -> dev; reg -> ci; reg -> wk; pol -> dev; pol -> ci; pol -> wk; dev -> aud; ci -> aud; wk -> aud; aud -> ana; fleet -> wk; dev -> fleet [label="offload task"];
  dev -> gw; wk -> gw; ci -> gw; gw -> vend; mreg -> dev; mreg -> wk;
}''' % BASE

D["registry_lifecycle"] = r'''
digraph G { rankdir=LR; %s
  dev [label="Develop\n(manifest + prompts\n+ schemas + evals)"]; run [label="Run locally\n(sandboxed)"]; ev [label="Evaluate\n(suite, thresholds)"]; sec [label="Security & policy\nreview"]; sign [label="Sign & publish\n(OCI artifact + signature)"]; asg [label="Assign\n(teams, workspaces)"]; dep [label="Deploy / pin\n(version, digest)"]; mon [label="Monitor\n(success, cost,\nviolations)"]; rb [label="Update or\nrollback"];
  dev -> run -> ev -> sec -> sign -> asg -> dep -> mon -> rb; rb -> dev [label="new version"];
}''' % BASE

D["ux_flow"] = r'''
digraph G { rankdir=TB; %s
  a [label="1. Request\n\"Add OAuth login with Google and GitHub\""];
  b [label="2. Exploring & planning (live timeline)\nrepo map · classification · model chosen (and why)"];
  c [label="3. Plan review (approval gate G1)\nsteps · files expected · risk · estimated cost\n[Approve] [Edit] [Cancel]", fillcolor="#FDE9D9"];
  d [label="4. Implementation (parallel worktrees)\nprogress per task · approval prompts inline\n(command outside allowlist, network egress)"];
  e [label="5. Verification\nbuild ✓ · tests 184/184 ✓ · static analysis ✓ · security review 0 blocking"];
  f [label="6. Review (approval gate G2)\ndiff viewer · reports · cost · audit trace\n[Apply to branch] [Commit] [Push (approval)] [Iterate]", fillcolor="#FDE9D9"];
  a -> b -> c -> d -> e -> f; f -> b [label="iterate", style=dashed];
}''' % BASE

D["routing_decision"] = r'''
digraph G { rankdir=TB; %s
  s [label="Task needs a model\n(class, required capabilities, data classification)", fillcolor="#DCE6F2"];
  c1 [label="Max provider tier allowed for this data?\nrestricted → T0/T1 · confidential → ≤T2 (T3 if org contract)\ninternal → ≤T3 · public → any", shape=diamond, fillcolor="#FFF8E7"];
  c2 [label="Filter: agent allow/deny lists\n∩ required capabilities (tool calling, JSON, context ≥ needed)", shape=diamond, fillcolor="#FFF8E7"];
  c3 [label="Rank remaining by policy strategy\nquality-first / cost-first / latency-first / prefer-internal", shape=diamond, fillcolor="#FFF8E7"];
  c4 [label="Provider healthy?\n(circuit breaker, rate-limit state)", shape=diamond, fillcolor="#FFF8E7"];
  ok [label="Use it · emit routing_decision event\n(candidates, chosen, reasons)", fillcolor="#EAF7EA"];
  nx [label="Next candidate in the SAME or LOWER tier\n(never widen the tier on fallback)"];
  none [label="No candidate → task waiting_for_input\n(user may pick a model or wait); never silent downgrade", fillcolor="#FDE9D9"];
  s -> c1 -> c2 -> c3 -> c4; c4 -> ok [label="yes"]; c4 -> nx [label="no"]; nx -> c4 [label="more"]; nx -> none [label="exhausted"];
}''' % BASE

D["harness_integration"] = r'''
digraph G { rankdir=LR; %s
  d [label="wardend\n(orchestrator, PDP, audit)", fillcolor="#FFF8E7"];
  subgraph cluster_sb { label="Sandbox (per task)"; style="rounded,filled"; fillcolor="#EAF7EA"; color="#5FA65F";
    h [label="Vendor harness process\ne.g. Copilot CLI (server mode)\nCodex app-server · claude -p"]; ex [label="warden-exec\n(tool executor)"]; wt [label="worktree (rw)"]; }
  v [label="Vendor endpoint\n(harness' own auth:\nsubscription / BYOK)", fillcolor="#F0F0F0"];
  d -> h [label="JSON-RPC session:\nprompt, custom tools,\nhooks (pre/post tool use,\npermission requests)"];
  h -> d [label="tool call / permission\nrequest → PDP decision", style=dashed];
  d -> ex [label="approved tool call"]; ex -> wt; h -> v [label="only allowlisted\nvendor domains via proxy"];
}''' % BASE

D["provenance_chain"] = r'''
digraph G { rankdir=LR; %s node [shape=record];
  e1 [label="{event 41|type: model.call|hash: 9c1…|prev: 7a0…}"]; e2 [label="{event 42|type: policy.decision|hash: 4e8…|prev: 9c1…}"]; e3 [label="{event 43|type: tool.exec|hash: b31…|prev: 4e8…}"]; e4 [label="{event 44|type: artifact.created|hash: 02f…|prev: b31…}"];
  cp [label="checkpoint (every N events / session end)\nsigned digest of last hash\n(local key; org key when control plane exists)", shape=box, fillcolor="#FFF8E7"];
  art [label="{Artifact code-diff:1a2b|derived_from: plan:88, research:12|produced_by: execution 7|model: gpt-x @ openai (T3)|routing_decision: event 41}", fillcolor="#EAF7EA"];
  e1 -> e2 -> e3 -> e4 -> cp; e4 -> art [style=dashed];
}''' % BASE

D["control_plane"] = r'''
digraph G { rankdir=TB; %s
  cp [label="Control plane services (Go, Postgres, object storage)", fillcolor="#DCE6F2", width=6];
  i [label="Identity & RBAC\nOIDC/SAML · roles: admin, security,\nplatform, developer, auditor"]; r [label="Registries\nagents (OCI) · models/providers\n· policies (signed bundles)"]; f [label="Fleet & jobs\nworker enrollment (mTLS)\njob queue · scheduling"]; a [label="Audit & analytics\nevent ingestion · retention\n· dashboards · exports"];
  ex [label="Execution plane: wardend instances (desktop, CI, workers) — pull bundles, push events", fillcolor="#FFF8E7", width=6];
  cp -> i; cp -> r; cp -> f; cp -> a; i -> ex; r -> ex; f -> ex; ex -> a;
}''' % BASE

D["eval_pipeline"] = r'''
digraph G { rankdir=LR; %s
  suite [label="Eval suite (YAML)\ncases: repo fixture (git bundle)\n+ prompt + graders + budget"]; run [label="warden eval run\nfresh sandbox per case\nagent@version × model set"]; grade [label="Graders\nbuild · tests · files-changed scope\nforbidden-path not accessed\nexpected denials · LLM judge (optional)"]; rep [label="Report\nsuccess rate · cost/success\nlatency · violations · regressions vs baseline"]; gate [label="Gate\nthresholds for publish / promote", shape=hexagon, fillcolor="#FDE9D9"];
  suite -> run -> grade -> rep -> gate;
}''' % BASE

D["workflow_lifecycle"] = r'''
digraph G { rankdir=LR; %s node [fontsize=9];
  intent -> context -> classify -> plan -> decompose -> delegate -> execute -> observe -> verify; verify -> repair [label="fail"]; repair -> execute; verify -> approve [label="pass"]; approve -> deliver -> audit;
  repair [style="rounded,filled,dashed"];
}''' % BASE

D["manifest_structure"] = r'''
digraph G { rankdir=LR; %s node [shape=record];
  m [label="{Agent package (directory / OCI artifact)|manifest.yaml (identity, capabilities, model policy, limits, approvals, artifacts)\lprompts/ (system, task templates)\lschemas/ (input, output, artifact JSON Schemas)\levals/ (suite.yaml + fixtures)\lsignature (digest + Ed25519 / sigstore)\l}"];
  wf [label="{Workflow definition|tasks[] (agent@ver, inputs, depends_on)\lretry, timeout, approval gates\lconcurrency groups\loutputs\l}"];
  impl [label="{Agent implementation (optional, Phase 4)|SDK process speaking Agent Protocol\lruns inside sandbox\lcalls back into runtime for model + tools\l}"];
  m -> wf [label="referenced by"]; m -> impl [label="may include"];
}''' % BASE

for name, dot in D.items():
    p = os.path.join(OUT, name + ".dot")
    with open(p, "w") as f: f.write(dot)
    r = subprocess.run(["dot", "-Tpng", p, "-o", os.path.join(OUT, name + ".png")], capture_output=True, text=True)
    if r.returncode != 0: print("ERR", name, r.stderr)
    else: print("ok", name)
