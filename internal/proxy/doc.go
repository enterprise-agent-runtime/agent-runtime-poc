// Package proxy is the per-task egress proxy: the only network exit for
// sandboxed processes. It implements design A07 (egress proxy) §3 (CONNECT
// and plain HTTP), §4 (target normalisation), §5 (allowlist), §7 (DNS,
// SSRF and rebinding protection), §9 (proxy.connect and proxy.denied) and
// §11 (credential-injection hook, never enabled), WRD-16 §10.4 and
// WRD-10 §7. CLAUDE.md §4 "Network" fixes the shape: deny by default, DNS
// by the proxy (C-03: the daemon, or the L2 sidecar, resolves; the sandbox
// has no resolver), model traffic never passes through here.
//
// Security properties, each with a test:
//
//   - Every target is normalised to one canonical host:port before any
//     decision (Normalize). IP literals in any form netip does not parse
//     (octal, hex, integer, short IPv4) are refused, and so is every name
//     whose last label is numeric, because the system resolver would read
//     "127.1" or "2130706433" as an address. Non-ASCII names are refused
//     (no IDNA library, D-033 candidate); "xn--" names are ordinary ASCII.
//   - Allowlist matching is exact equality on the canonical key. There are
//     no wildcard, suffix or CIDR entries (A07 §5.1); Allowlist.Add refuses
//     a key containing "*".
//   - The proxy resolves a name once, drops every address in a denied range
//     (A07 §7 plus the owner's 2026-10-16 additions, including IPv4-mapped,
//     NAT64 and 6to4 embeddings) and every address of the host's own
//     interfaces, and dials the checked address, never the name, so a
//     second DNS answer cannot change where the connection goes (DNS
//     rebinding).
//   - A floor (loopback, unspecified, link-local, cloud metadata) is never
//     dialed, whatever allowed it. Other denied-range literals are dialed
//     only when allowed (allowlist or Decider) and also named by
//     Config.OperatorLiteralAllow, never in a harness sandbox (owner
//     decision 2026-10-16, contradicting A07 §7; round-2 review F2).
//   - A CONNECT tunnel to a DNS name must open with a TLS ClientHello whose
//     server_name equals that name (A07 §3.2 step 6), and no ClientHello may
//     carry encrypted_client_hello (owner decision 2026-10-16); anything else
//     closes the tunnel with sni_mismatch or not_tls. TLS is never
//     terminated, so after a valid ClientHello the tunnel is not inspected
//     (domain fronting through the inner Host is an accepted residual).
//   - Every attempt ends in exactly one proxy.connect or proxy.denied event
//     through the injected EventSink, written with a context that shutdown
//     does not cancel. Events carry host, port, method and counters only:
//     never paths, query strings, headers, bodies or userinfo.
//   - Per task at most Limits.MaxConns connections run past evaluation, and
//     tunnels and plain-HTTP bodies that stall for Limits.IdleTimeout are
//     closed (A07 §10).
//
// The package opens no listener (CLAUDE.md §5 INV-J): Serve takes a
// net.Listener the caller owns (a per-task Unix socket in L1, M2-12a; the
// sidecar's listener in cmd/warden-proxy for L2).
//
// M2 ships StaticDecider, which denies every destination that is not on the
// allowlist. The PDP hand-off and the 120 s approval hold (A07 §6), the deny
// cache and rate-limit summaries arrive with the policy engine in M3.
package proxy
