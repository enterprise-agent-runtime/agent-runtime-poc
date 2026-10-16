# B06 Visual design

This file defines the visual language of the Warden desktop app: design tokens with exact values for both themes, the semantic encodings for effects, trust tiers, task states and classifications, typography, spacing, radii, borders, elevation, density, iconography and motion. B03 (wireframes), B04 (components) and B05 (interaction) reference the token names defined here and in `00-DESIGN-CORE.md` §11; they never use raw values. The token set is also given as JSON (§12) for import into a design-system tool and as the source for the generated CSS custom properties (§11).

## 1. Principles

1. **A professional instrument, not a chat app.** Calm neutrals, one accent, dense but legible rows, no gradients, no illustrations, no avatars, no speech bubbles. Colour is reserved for meaning: effects, states, tiers, classifications and diffs. Anything decorative is grey.
2. **Never hue alone.** Every semantic distinction is carried by at least three channels: a colour token, an icon from the fixed icon map (§8), and a text label. Tiers and classifications add a fourth channel, the chip shape (filled versus outline, solid versus dashed border), and a lightness order. A user with any form of colour-vision deficiency, or a greyscale screenshot in the PoC report, must lose nothing (WCAG 2.2 SC 1.4.1).
3. **Trust reads as weight.** Things inside the company boundary (tiers T0 to T2, the `confidential` classification, granted effects) look heavier: filled chips and more prominent lightness (darker in the light theme, brighter in the dark theme). Things outside (T3, T4) look lighter: outline chips with dashed borders. The order T0 → T4 is monotone in luminance in both themes (verified in §4).
4. **Decision before effect is visible in the layout.** Decision colours (allow, deny, approval) appear on the left edge of the row or card that they govern, before the effect's own output (WRD-11 §1 principle 1).
5. **Both themes are first-class.** Every token has a light and a dark value; every contrast pair is verified in both. The app follows the OS setting by default and offers `System`, `Light`, `Dark` in Settings.
6. **Accessible by construction.** Text pairs meet 4.5:1, UI component boundaries and focus indicators meet 3:1 (WCAG 2.2 SC 1.4.3, 1.4.11); targets are at least 24 × 24 px (SC 2.5.8); motion respects `prefers-reduced-motion` and never blinks for more than 5 s (SC 2.2.2, 2.3.3).

## 2. Colour tokens

Token names in `00-DESIGN-CORE.md` §11 are mandatory; tokens marked NEW are additions this file needs (listed in Deviations). Values are sRGB hex; `color-scrim` uses 8-digit hex with alpha. The light theme is designed on `color-surface-1` `#FFFFFF`; the dark theme on `color-surface-1` `#161B22`.

| Token | Light | Dark | Usage |
|---|---|---|---|
| `color-bg` | `#F6F7F9` | `#0D1117` | App background behind panels (window chrome, gutters between panels). |
| `color-surface-1` | `#FFFFFF` | `#161B22` | Primary panel and card surface: timeline, context panel, cards, dialogs. |
| `color-surface-2` | `#F0F2F5` | `#1D232C` | Secondary surface: hovered row, section headers, input fields, internal-classification chip fill, code gutters. |
| `color-surface-3` | `#E4E8ED` | `#262D37` | Tertiary surface: pressed or selected-inactive row, skeleton blocks, kbd hint background. |
| `color-border` | `#D8DDE3` | `#2D3440` | Decorative dividers and card outlines where the edge is not the only cue (no contrast requirement). |
| `color-border-strong` | `#7D8898` | `#6E7A8B` | Boundaries of interactive controls (inputs, checkboxes, scope selector segments, outline chips); meets 3:1. |
| `color-text` | `#1A1F26` | `#E6EAF0` | Primary text and icons. |
| `color-text-muted` | `#56606D` | `#9BA5B3` | Secondary text: timestamps, elapsed time, metadata, placeholder, disabled-reason lines. |
| `color-text-inverse` | `#FFFFFF` | `#0D1117` | Text and icons on filled accent, confidential chip and filled state pills. |
| `color-focus` | `#1B62D6` | `#79B0FF` | 2 px focus ring (outline with 2 px offset) on every focusable element. |
| `color-accent` | `#1F5FC8` | `#6EA8FE` | Primary button fill, links, selected timeline entry indicator bar, running progress. |
| `color-accent-hover` (NEW) | `#194FA8` | `#9AC2FF` | Primary button hover and pressed fill. |
| `color-accent-subtle` (NEW) | `#E8F0FC` | `#1A2A42` | Selected timeline row background, selected file in diff file list. |
| `color-effect-allow` | `#1A7432` | `#56D364` | Effect allow: icon and text in policy.decision rows, allow chips. |
| `color-effect-allow-bg` (NEW) | `#EAF6EC` | `#15291C` | Tint behind allow chips and granted-approval rows. |
| `color-effect-deny` | `#B3261E` | `#FF8A80` | Effect deny: icon and text in denied tool call rows, proxy.denied, deny chips. |
| `color-effect-deny-bg` (NEW) | `#FCEDEB` | `#3A1A1A` | Tint behind deny chips and denied rows; ST-6 banner background. |
| `color-effect-approval` | `#8F5200` | `#F0B64D` | Effect approval_required: ApprovalCard stripe, pending badge, approval chips; also waiting state. |
| `color-effect-approval-bg` (NEW) | `#FFF4E0` | `#33260F` | ApprovalCard background, pending-approval badge fill tint. |
| `color-untrusted` | `#6E3FAF` | `#C9A8FF` | Untrusted-data marker on observations (file content, command output, harness output) and taint notes. |
| `color-untrusted-bg` (NEW) | `#F3EDFB` | `#2A2140` | Tint behind untrusted-output blocks in the context panel. |
| `color-tier-t0` | `#08403B` | `#9BEBDF` | Tier T0 local: chip text, icon, border. Darkest in light theme, brightest in dark theme (most inside). |
| `color-tier-t1` | `#0B554F` | `#78D6C9` | Tier T1 company-hosted: chip text, icon, border. |
| `color-tier-t2` | `#0E6760` | `#58BFB2` | Tier T2 company cloud tenant: chip text, icon, border. |
| `color-tier-t3` | `#5A647D` | `#9CA3B8` | Tier T3 vendor API: outline chip text, icon, dashed border (outside the company). |
| `color-tier-t4` | `#666E84` | `#868DA2` | Tier T4 subscription harness: outline chip text, icon, dashed border (outside, least control). |
| `color-tier-inside-bg` (NEW) | `#E3F3F0` | `#10302C` | Fill of T0 to T2 tier chips and of the 'Inside the company' group header in ModelPicker. |
| `color-state-running` | `#1F5FC8` | `#6EA8FE` | Task state running: spinner, step counter, left stripe. |
| `color-state-waiting` | `#8F5200` | `#F0B64D` | Task states waiting_for_approval and waiting_for_input: icon, pulse ring, stripe. |
| `color-state-succeeded` | `#1A7432` | `#56D364` | Task state succeeded; doctor check ok; chain verified. |
| `color-state-failed` | `#B3261E` | `#FF8A80` | Task states failed and timed_out; doctor check fail; chain failed. |
| `color-state-cancelled` | `#5F6773` | `#9BA5B3` | Task state cancelled, skipped, blocked; greyed entries after ST-5. |
| `color-diff-add` | `#E6F4EA` | `#12261A` | Background of added lines in DiffViewer. |
| `color-diff-del` | `#FCECEA` | `#2E1616` | Background of deleted lines in DiffViewer. |
| `color-diff-add-strong` (NEW) | `#1A7432` | `#56D364` | Plus sign in the add gutter and the add count in file stats. |
| `color-diff-del-strong` (NEW) | `#B3261E` | `#FF8A80` | Minus sign in the delete gutter and the delete count in file stats. |
| `color-diff-add-word` (NEW) | `#C4E7CD` | `#1E4A2B` | Intra-line changed-word highlight on added lines. |
| `color-diff-del-word` (NEW) | `#F6CBC6` | `#5A2525` | Intra-line changed-word highlight on deleted lines. |
| `color-class-public` | `#4E5866` | `#A7B0BD` | Classification public: outline chip text, icon and border. |
| `color-class-internal` | `#1C4E9E` | `#8DB8FF` | Classification internal: chip text and icon on surface-2 fill with solid border. |
| `color-class-confidential` | `#8A1C55` | `#F2A7CF` | Classification confidential: solid chip fill (text-inverse on top) and lock icon elsewhere. |
| `color-scrim` (NEW) | `#0D111766` | `#000000A3` | Backdrop behind confirmation dialogs (not behind approvals, which are inline). |

### 2.1 Surface model

| Layer | Light | Dark | Used for |
|---|---|---|---|
| Window | `color-bg` | `color-bg` | Behind panels, gutters between timeline and context panel |
| Panel / card | `color-surface-1` | `color-surface-1` | Timeline column, context panel, cards, dialogs |
| Raised inside a card | `color-surface-2` | `color-surface-2` | Inputs, code blocks, section headers, hovered rows, menus in dark theme |
| Pressed / overlay | `color-surface-3` | `color-surface-3` | Pressed rows, skeletons, dialogs in dark theme |

In the light theme depth comes from shadows (`elevation-1..3`); in the dark theme depth comes from the surface step (each step is lighter) plus a 1 px inner highlight (`elevation-1-dark..3-dark`), because dark shadows are nearly invisible.

## 3. Semantic encodings

### 3.1 Policy effects and untrusted data

| Concept | Colour (fg) | Tint (bg) | Icon (Lucide) | Label (B07 key) | Shape |
|---|---|---|---|---|---|
| `allow` | `color-effect-allow` | `color-effect-allow-bg` | `shield-check` | "Allowed" (`effect.allow`) | Chip; rows show no stripe (allow is the quiet default) |
| `deny` | `color-effect-deny` | `color-effect-deny-bg` | `ban` | "Denied" (`effect.deny`) | Chip; 3 px left stripe on the ToolCallRow; row text struck through is **not** used (legibility) |
| `approval_required` | `color-effect-approval` | `color-effect-approval-bg` | `hand` | "Needs approval" (`effect.approval_required`) | ApprovalCard with 4 px left stripe and tinted background; chip elsewhere |
| Untrusted observation (BI-4) | `color-untrusted` | `color-untrusted-bg` | `triangle-alert` | "Untrusted output" (`provenance.untrusted`) | Tinted block with a 2 px dashed left border around any file content, command output or harness output shown in the context panel |

The allow row is deliberately low-key: a green shield icon in a 16 px column, no tint. H6 needs the user to spot the exceptions (deny and approval) in a list that is mostly allows.

### 3.2 Trust tiers (ordered)

| Tier | Group | Colour | Icon | Chip label (long / short) | Chip shape | Relative luminance light / dark |
|---|---|---|---|---|---|---|
| T0 | Inside the company | `color-tier-t0` | `laptop` | "T0 Local" / "T0" | Filled `color-tier-inside-bg`, 1 px solid border in tier colour | 0.040 / 0.717 |
| T1 | Inside the company | `color-tier-t1` | `server` | "T1 Company-hosted" / "T1" | Filled, solid border | 0.071 / 0.563 |
| T2 | Inside the company | `color-tier-t2` | `cloud-cog` | "T2 Company cloud" / "T2" | Filled, solid border | 0.106 / 0.426 |
| T3 | Outside the company | `color-tier-t3` | `globe` | "T3 Vendor API" / "T3" | Outline on `color-surface-1`, 1 px **dashed** border | 0.128 / 0.367 |
| T4 | Outside the company | `color-tier-t4` | `plug` | "T4 Subscription" / "T4" | Outline, dashed border | 0.156 / 0.267 |

Rules:

- The luminance column shows the ordering is strictly monotone: in the light theme T0 is darkest and T4 lightest; in the dark theme T0 is brightest and T4 dimmest. "More inside" always means "more prominent".
- Inside tiers share a teal hue family; outside tiers share a slate hue family. Hue is a supporting cue only; the fill-versus-outline and solid-versus-dashed distinction plus the icon and text carry the meaning without colour.
- The short chip ("T1" plus icon) is used in dense rows (RoutingLine, ToolCallRow provenance, CostPanel rows). It always carries `aria-label` "Tier T1, company-hosted, inside the company" (B07 key `tier.aria`) and a tooltip with the long label.
- ModelPicker groups models under two headings: "Inside the company (T0 to T2)" on a `color-tier-inside-bg` header band and "Outside the company (T3, T4)" on a plain header with a dashed top rule. Inadmissible models keep their tier chip at full contrast (the tier is information) while the model name and the reason line use `color-text-muted` plus the `ban` icon and the reason text (B07 `routing.reject.*`); the row is `aria-disabled="true"`, never `opacity` below 1 (opacity would break contrast).

### 3.3 Task states

| State(s) | Colour | Icon | Label | Additional cue |
|---|---|---|---|---|
| `created`, `queued` | `color-text-muted` | `circle-dashed` | "Queued" | none |
| `running` | `color-state-running` | `loader-circle` (rotating; static `circle-dot` under reduced motion) | "Running · step {n}/{max}" | 3 px left stripe on TaskCard |
| `waiting_for_approval` | `color-state-waiting` | `hand` | "Waiting for approval" | Pulse ring (§9.3), stripe, PendingApprovalBadge count |
| `waiting_for_input` | `color-state-waiting` | `circle-help` | "Waiting for input" | Stripe; reason line always visible |
| `succeeded` | `color-state-succeeded` | `circle-check` | "Succeeded" | none |
| `failed` | `color-state-failed` | `circle-x` | "Failed: {reason}" | 3 px stripe |
| `timed_out` | `color-state-failed` | `timer-off` | "Timed out" | 3 px stripe |
| `cancelled` | `color-state-cancelled` | `circle-slash` | "Cancelled" | Entry text switches to `color-text-muted` (ST-5 greyed entries) |
| `skipped`, `blocked` | `color-state-cancelled` | `circle-minus` | "Skipped" / "Blocked: {reason}" | none |

Workflow run status in the header uses the same mapping (`running` → running, `waiting` → waiting, `succeeded`, `failed`, `cancelled`).

### 3.4 Data classification

| Classification | Colour | Icon | Label | Chip shape (lightness order) |
|---|---|---|---|---|
| `public` | `color-class-public` | `book-open` | "Public" | Outline on surface, 1 px solid `color-class-public` border (lightest) |
| `internal` | `color-class-internal` | `building-2` | "Internal" | `color-surface-2` fill, 1 px solid border in class colour (middle) |
| `confidential` | `color-class-confidential` | `lock` | "Confidential" | Solid `color-class-confidential` fill, `color-text-inverse` text and icon (heaviest) |

Confidential is the heaviest element in the header on purpose: it is the classification that changes routing (BI-7). The same chip appears in the session header, WorkspaceRow and the classification confirmation dialog.

### 3.5 Chain status, sandbox and doctor

| Concept | Colour | Icon | Label |
|---|---|---|---|
| Chain `verified` | `color-state-succeeded` | `link` | "Chain verified" |
| Chain `unverified` | `color-text-muted` | `link` | "Chain not verified yet" |
| Chain `verifying` | `color-state-running` | `loader-circle` | "Verifying chain…" |
| Chain `failed` (ST-6) | `color-state-failed` | `link-2-off` | "Chain verification failed" |
| Sandbox L1 | `color-text` | `box` | "L1 (Seatbelt)" / "L1 (bubblewrap)" |
| Sandbox L2 | `color-text` | `container` | "L2 (Docker)" |
| Doctor `ok` / `warn` / `fail` | `color-state-succeeded` / `color-state-waiting` / `color-state-failed` | `circle-check` / `circle-alert` / `circle-x` | "OK" / "Warning" / "Blocking" or "Failed" |

### 3.6 Diff

| Element | Token | Non-colour cue |
|---|---|---|
| Added line background | `color-diff-add` | `+` in the sign gutter in `color-diff-add-strong` |
| Deleted line background | `color-diff-del` | `−` (U+2212) in the sign gutter in `color-diff-del-strong` |
| Changed word on added / deleted line | `color-diff-add-word` / `color-diff-del-word` | Underline 1 px in the strong colour under changed words (visible in greyscale) |
| Hunk header | `color-surface-2`, text `color-text-muted`, `mono-sm` | Provenance link chip at the right end (task key + step, `git-commit-horizontal` icon) |
| File stats | `+12` in `color-diff-add-strong`, `−3` in `color-diff-del-strong` | Signs always printed |

## 4. Contrast verification

Method: WCAG 2.x relative luminance (sRGB linearisation with threshold 0.04045) and ratio `(L1 + 0.05) / (L2 + 0.05)`, computed by a script over every pair below (the script lives in the repository as `apps/desktop/scripts/check-contrast.py`, NEW, and runs in CI against `tokens.json`; any pair below its minimum fails the build). Minimums: 4.5:1 for text and icons that carry text meaning (all semantic icons are paired with text, but they are held to the text minimum anyway); 3:1 for UI component boundaries and the focus indicator. `color-border` is decorative (card edges that are also separated by surface colour or spacing) and has no requirement; any boundary a user must perceive to operate a control uses `color-border-strong`.

Result: all 89 pairs pass in both themes. Lowest text pair: `color-tier-t4` on `color-surface-2` (4.54:1 light, 4.77:1 dark); this is why tier chips never sit on `color-surface-3`. Lowest UI pair: `color-border-strong` on `color-surface-2` (3.20:1 light, 3.63:1 dark). Any new pairing a component introduces must be added to the script before use.

| Foreground | Background | Kind (min) | Light | Dark |
|---|---|---|---|---|
| `color-text` | `color-bg` | text (4.5:1) | 15.45:1 | 15.67:1 |
| `color-text` | `color-surface-1` | text (4.5:1) | 16.56:1 | 14.33:1 |
| `color-text` | `color-surface-2` | text (4.5:1) | 14.77:1 | 13.09:1 |
| `color-text` | `color-surface-3` | text (4.5:1) | 13.46:1 | 11.50:1 |
| `color-text-muted` | `color-bg` | text (4.5:1) | 5.96:1 | 7.59:1 |
| `color-text-muted` | `color-surface-1` | text (4.5:1) | 6.38:1 | 6.94:1 |
| `color-text-muted` | `color-surface-2` | text (4.5:1) | 5.69:1 | 6.34:1 |
| `color-text-muted` | `color-surface-3` | text (4.5:1) | 5.19:1 | 5.57:1 |
| `color-text-inverse` | `color-accent` | text (4.5:1) | 5.95:1 | 7.84:1 |
| `color-text-inverse` | `color-accent-hover` | text (4.5:1) | 7.71:1 | 10.39:1 |
| `color-text-inverse` | `color-class-confidential` | text (4.5:1) | 8.83:1 | 10.10:1 |
| `color-accent` | `color-bg` | text (4.5:1) | 5.55:1 | 7.84:1 |
| `color-accent` | `color-surface-1` | text (4.5:1) | 5.95:1 | 7.16:1 |
| `color-accent` | `color-accent-subtle` | text (4.5:1) | 5.19:1 | 5.98:1 |
| `color-text` | `color-accent-subtle` | text (4.5:1) | 14.43:1 | 11.96:1 |
| `color-text-muted` | `color-accent-subtle` | text (4.5:1) | 5.56:1 | 5.80:1 |
| `color-effect-allow` | `color-bg` | text (4.5:1) | 5.46:1 | 9.82:1 |
| `color-effect-allow` | `color-surface-1` | text (4.5:1) | 5.85:1 | 8.98:1 |
| `color-effect-allow` | `color-surface-2` | text (4.5:1) | 5.22:1 | 8.20:1 |
| `color-effect-allow` | `color-effect-allow-bg` | text (4.5:1) | 5.27:1 | 7.98:1 |
| `color-text` | `color-effect-allow-bg` | text (4.5:1) | 14.90:1 | 12.73:1 |
| `color-effect-deny` | `color-bg` | text (4.5:1) | 6.10:1 | 8.29:1 |
| `color-effect-deny` | `color-surface-1` | text (4.5:1) | 6.54:1 | 7.58:1 |
| `color-effect-deny` | `color-surface-2` | text (4.5:1) | 5.83:1 | 6.92:1 |
| `color-effect-deny` | `color-effect-deny-bg` | text (4.5:1) | 5.74:1 | 6.85:1 |
| `color-text` | `color-effect-deny-bg` | text (4.5:1) | 14.55:1 | 12.95:1 |
| `color-effect-approval` | `color-bg` | text (4.5:1) | 5.80:1 | 10.37:1 |
| `color-effect-approval` | `color-surface-1` | text (4.5:1) | 6.22:1 | 9.48:1 |
| `color-effect-approval` | `color-surface-2` | text (4.5:1) | 5.55:1 | 8.66:1 |
| `color-effect-approval` | `color-effect-approval-bg` | text (4.5:1) | 5.71:1 | 8.07:1 |
| `color-text` | `color-effect-approval-bg` | text (4.5:1) | 15.20:1 | 12.21:1 |
| `color-untrusted` | `color-bg` | text (4.5:1) | 6.55:1 | 9.49:1 |
| `color-untrusted` | `color-surface-1` | text (4.5:1) | 7.02:1 | 8.67:1 |
| `color-untrusted` | `color-surface-2` | text (4.5:1) | 6.26:1 | 7.92:1 |
| `color-untrusted` | `color-untrusted-bg` | text (4.5:1) | 6.12:1 | 7.57:1 |
| `color-text` | `color-untrusted-bg` | text (4.5:1) | 14.45:1 | 12.51:1 |
| `color-tier-t0` | `color-surface-1` | text (4.5:1) | 11.62:1 | 12.64:1 |
| `color-tier-t0` | `color-surface-2` | text (4.5:1) | 10.36:1 | 11.54:1 |
| `color-tier-t0` | `color-tier-inside-bg` | text (4.5:1) | 10.15:1 | 10.36:1 |
| `color-tier-t1` | `color-surface-1` | text (4.5:1) | 8.65:1 | 10.10:1 |
| `color-tier-t1` | `color-surface-2` | text (4.5:1) | 7.72:1 | 9.23:1 |
| `color-tier-t1` | `color-tier-inside-bg` | text (4.5:1) | 7.56:1 | 8.28:1 |
| `color-tier-t2` | `color-surface-1` | text (4.5:1) | 6.71:1 | 7.83:1 |
| `color-tier-t2` | `color-surface-2` | text (4.5:1) | 5.99:1 | 7.16:1 |
| `color-tier-t2` | `color-tier-inside-bg` | text (4.5:1) | 5.87:1 | 6.42:1 |
| `color-tier-t3` | `color-bg` | text (4.5:1) | 5.51:1 | 7.52:1 |
| `color-tier-t3` | `color-surface-1` | text (4.5:1) | 5.91:1 | 6.87:1 |
| `color-tier-t3` | `color-surface-2` | text (4.5:1) | 5.27:1 | 6.28:1 |
| `color-tier-t4` | `color-bg` | text (4.5:1) | 4.75:1 | 5.72:1 |
| `color-tier-t4` | `color-surface-1` | text (4.5:1) | 5.09:1 | 5.23:1 |
| `color-tier-t4` | `color-surface-2` | text (4.5:1) | 4.54:1 | 4.77:1 |
| `color-state-running` | `color-surface-1` | text (4.5:1) | 5.95:1 | 7.16:1 |
| `color-state-running` | `color-surface-2` | text (4.5:1) | 5.31:1 | 6.54:1 |
| `color-state-waiting` | `color-surface-1` | text (4.5:1) | 6.22:1 | 9.48:1 |
| `color-state-waiting` | `color-surface-2` | text (4.5:1) | 5.55:1 | 8.66:1 |
| `color-state-succeeded` | `color-surface-1` | text (4.5:1) | 5.85:1 | 8.98:1 |
| `color-state-succeeded` | `color-surface-2` | text (4.5:1) | 5.22:1 | 8.20:1 |
| `color-state-failed` | `color-surface-1` | text (4.5:1) | 6.54:1 | 7.58:1 |
| `color-state-failed` | `color-surface-2` | text (4.5:1) | 5.83:1 | 6.92:1 |
| `color-state-cancelled` | `color-surface-1` | text (4.5:1) | 5.72:1 | 6.94:1 |
| `color-state-cancelled` | `color-surface-2` | text (4.5:1) | 5.10:1 | 6.34:1 |
| `color-text` | `color-diff-add` | text (4.5:1) | 14.59:1 | 13.20:1 |
| `color-text` | `color-diff-add-word` | text (4.5:1) | 12.35:1 | 8.40:1 |
| `color-diff-add-strong` | `color-diff-add` | text (4.5:1) | 5.15:1 | 8.27:1 |
| `color-text` | `color-diff-del` | text (4.5:1) | 14.45:1 | 14.00:1 |
| `color-text` | `color-diff-del-word` | text (4.5:1) | 11.26:1 | 10.08:1 |
| `color-diff-del-strong` | `color-diff-del` | text (4.5:1) | 5.70:1 | 7.40:1 |
| `color-text-muted` | `color-diff-add` | text (4.5:1) | 5.62:1 | 6.39:1 |
| `color-text-muted` | `color-diff-del` | text (4.5:1) | 5.57:1 | 6.78:1 |
| `color-class-public` | `color-surface-1` | text (4.5:1) | 7.21:1 | 7.90:1 |
| `color-class-public` | `color-bg` | text (4.5:1) | 6.73:1 | 8.64:1 |
| `color-class-internal` | `color-surface-2` | text (4.5:1) | 7.11:1 | 7.85:1 |
| `color-class-internal` | `color-surface-1` | text (4.5:1) | 7.98:1 | 8.59:1 |
| `color-class-confidential` | `color-surface-1` | text (4.5:1) | 8.83:1 | 9.23:1 |
| `color-border-strong` | `color-bg` | UI (3.0:1) | 3.35:1 | 4.34:1 |
| `color-border-strong` | `color-surface-1` | UI (3.0:1) | 3.59:1 | 3.97:1 |
| `color-border-strong` | `color-surface-2` | UI (3.0:1) | 3.20:1 | 3.63:1 |
| `color-focus` | `color-bg` | UI (3.0:1) | 5.20:1 | 8.53:1 |
| `color-focus` | `color-surface-1` | UI (3.0:1) | 5.58:1 | 7.80:1 |
| `color-focus` | `color-surface-2` | UI (3.0:1) | 4.97:1 | 7.12:1 |
| `color-focus` | `color-surface-3` | UI (3.0:1) | 4.53:1 | 6.26:1 |
| `color-focus` | `color-effect-approval-bg` | UI (3.0:1) | 5.12:1 | 6.64:1 |
| `color-focus` | `color-accent-subtle` | UI (3.0:1) | 4.86:1 | 6.51:1 |
| `color-accent` | `color-surface-1` | UI (3.0:1) | 5.95:1 | 7.16:1 |
| `color-tier-t3` | `color-surface-1` | UI (3.0:1) | 5.91:1 | 6.87:1 |
| `color-tier-t4` | `color-surface-1` | UI (3.0:1) | 5.09:1 | 5.23:1 |
| `color-effect-approval` | `color-surface-1` | UI (3.0:1) | 6.22:1 | 9.48:1 |
| `color-class-confidential` | `color-surface-1` | UI (3.0:1) | 8.83:1 | 9.23:1 |
| `color-class-confidential` | `color-bg` | UI (3.0:1) | 8.24:1 | 10.10:1 |

## 5. Typography

### 5.1 Families

| Role | Stack | Delivery |
|---|---|---|
| UI sans | `"Inter", system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif` | Inter 4.x variable font (`InterVariable.woff2`, weights 400 to 600 used), bundled in the app under `apps/desktop/src/assets/fonts/`, subset to Latin + Latin Extended-A (covers English, Romanian ș ț ă â î and German ä ö ü ß for WRD-11 §6). Loaded with `@font-face { font-display: block }` from the app bundle; never from a CDN (CSP forbids remote fonts, B08 §6). |
| Mono | `"JetBrains Mono", ui-monospace, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace` | JetBrains Mono 2.3 variable (`JetBrainsMono[wght].woff2`, 400 and 600), same subset and delivery. Ligatures off (`font-variant-ligatures: none`) so that `!=`, `->` and `=>` in diffs show exactly the characters in the file. |

Why bundled rather than system fonts only: Inter and JetBrains Mono have tabular figures and unambiguous glyphs (`0/O`, `1/l/I`) which matter for ids, hashes and costs; bundling gives identical metrics on macOS and Linux, so row heights in §6 hold on both. The system stacks remain as fallbacks if a font file fails to load.

Font features: all numeric content (costs, token counts, step counters, elapsed time, test counts, luminance values) uses `font-variant-numeric: tabular-nums` so that live-updating numbers do not shift layout (B05 streaming without layout jumps). Inter's `cv11` (single-storey a) is off; `ss01` (open digits) is on for the sans.

### 5.2 Styles

| Style | Family | Size / line height | Weight | Use |
|---|---|---|---|---|
| `text-xs` | sans | 11 / 16 px | 500 | Badges, chips, kbd hints, counters. Minimum size in the app. |
| `text-sm` | sans | 12 / 16 px | 400 | Metadata: timestamps, elapsed time, cost badges, routing line detail, dense table cells |
| `text-sm-strong` (NEW) | sans | 12 / 16 px | 600 | Column headers, ModelPicker group headings, emphasised chip labels |
| `text-md` | sans | 13 / 20 px | 400 | Default UI text: timeline entry titles, card body, form fields, buttons |
| `text-md-strong` (NEW) | sans | 13 / 20 px | 600 | Card titles, the ApprovalCard "what" label, primary button labels |
| `text-lg` | sans | 15 / 22 px | 600 | Panel titles ("Review the plan", "Review the result", context panel header) |
| `text-xl` | sans | 18 / 24 px | 600 | Screen title, one per screen |
| `mono-sm` | mono | 12 / 16 px | 400 | Ids (`call_…`, `apr_…`), rule ids, hashes, paths in dense rows, argv in ToolCallRow |
| `mono-md` | mono | 13 / 20 px | 400 | Diff lines, the exact command/path/host in the ApprovalCard, plan JSON editor, failure excerpts |

Rules:

- Sentence case everywhere (B07 §1); no all-caps labels, no letter-spacing tricks. Weight 600 is the only emphasis weight; italics are reserved for the untrusted-output caption.
- Truncation: paths truncate in the middle (`src/…/users.test.ts`) with the full value in a tooltip and in the accessible name; free text truncates at the end with an ellipsis; ids never truncate in the ApprovalCard, PlanCard or ExplainDrawer.
- Rule ids, tool ids, model ids, provider ids and file paths are always set in mono, so they can be copied into a support request exactly (B07 voice rule).
- Text zoom: the layout supports 200 % zoom (SC 1.4.4) via the webview zoom; rows grow in height, nothing is clipped, because heights are `min-height` values (§6).

## 6. Spacing, layout and density

### 6.1 Spacing scale (4 px base)

| Token | Value | Typical use |
|---|---|---|
| `space-1` | 4 px | Icon-to-label gap in chips, gap between stacked badges |
| `space-2` | 8 px | Default inline gap; chip and compact button horizontal padding |
| `space-3` | 12 px | Horizontal padding of timeline rows and inputs; gap between card sections |
| `space-4` | 16 px | Card padding, panel gutters, gap between cards |
| `space-5` | 20 px | Indentation of nested entries (tool calls under a TaskCard) |
| `space-6` | 24 px | Panel padding at ≥ 1440 px, dialog padding |
| `space-7` | 32 px | Section separation in Settings and Doctor |
| `space-8` | 48 px | Empty-state vertical padding, setup content top offset |

### 6.2 Density

The timeline is the most-read surface; it defaults to **compact**. A per-user setting `density: compact | comfortable` (Settings → Appearance, stored locally; NEW) switches the row metrics below. Heights are `min-height` so that zoom and wrapped text grow rows rather than clip them; TanStack Virtual measures actual heights (B08 §8).

| Element | Compact (default) | Comfortable |
|---|---|---|
| TimelineEntry (single line: icon 16 px, `text-md`) | 28 px, padding 4 px vertical, `space-3` horizontal | 32 px, 6 px vertical |
| ToolCallRow (collapsed) | 28 px, indent `space-5` | 32 px |
| TaskCard header (state, title, step counter, elapsed, cost) | 32 px | 36 px |
| RoutingLine | 24 px (`text-sm`) | 28 px |
| Table rows (Settings, Doctor, ModelPicker options) | 32 px | 36 px |
| Icon button hit area | 24 × 24 px (SC 2.5.8 minimum) | 28 × 28 px |
| Primary / secondary buttons | 28 px high, `space-3` horizontal padding | 32 px |
| ApprovalCard and gate buttons (Approve, Reject, scope segments) | 32 px (never compact: these are the high-consequence controls) | 36 px |

### 6.3 Layout constants (informative; B03 is authoritative for placement)

| Constant | Value |
|---|---|
| Supported window width | 1024 to 1920 px (min window 1024 × 640) |
| App header height | 44 px |
| Timeline column | min 480 px, flexible |
| Context panel | 360 px at 1024 px width, 440 px at 1440, max 560 px; user-resizable with a 4 px drag handle (24 px hit area) |
| Readable measure for prose (plan summary, analysis) | max 72 ch |
| GateBar and DeliveryBar (sticky bottom) | 56 px; the timeline scroller sets `scroll-padding-bottom: 56px` so a focused row is never hidden behind it (SC 2.4.11) |

## 7. Radii, borders, focus and elevation

| Token | Value | Use |
|---|---|---|
| `radius-sm` | 4 px | Chips, badges, kbd hints, inputs, compact buttons, code blocks |
| `radius-md` | 6 px | Cards, buttons, menus, popovers |
| `radius-lg` | 10 px | Dialogs, drawers, Banner, SetupWizard panel |
| `radius-pill` (NEW) | 999 px | PendingApprovalBadge count, step counter pill |

Borders: 1 px everywhere. Card outlines use `color-border`; controls use `color-border-strong`. State stripes are 3 px (TaskCard, denied ToolCallRow) and 4 px (ApprovalCard, gate cards) on the left edge, inside the card radius. Dashed borders are reserved for outside tiers (T3, T4) and untrusted output blocks, so "dashed" consistently means "outside our control".

Focus indicator: `outline: 2px solid var(--color-focus); outline-offset: 2px;` on `:focus-visible` for every focusable element, including timeline rows (roving tabindex, B05) and diff lines. Inside the ApprovalCard the selected scope segment additionally gets a 2 px inner border in `color-accent` so that "selected" and "focused" are distinguishable. Focus is never removed; `outline: none` is a lint error (stylelint rule in B08 §11).

Elevation:

| Token | Light value | Dark equivalent | Use |
|---|---|---|---|
| `elevation-1` | `0 1px 2px 0 #0F172A14` | `elevation-1-dark` `0 0 0 1px #FFFFFF0F` on `color-surface-1` | Resting cards |
| `elevation-2` | `0 4px 12px -2px #0F172A1F, 0 2px 4px -2px #0F172A14` | `elevation-2-dark` on `color-surface-2` | Menus, popovers, ModelPicker listbox, tooltips, sticky GateBar |
| `elevation-3` | `0 16px 40px -8px #0F172A33, 0 4px 12px -4px #0F172A1F` | `elevation-3-dark` on `color-surface-3` | Dialogs, ExplainDrawer |

The CSS variable `--elevation-N` resolves to the light or dark value according to the active theme (§11), so components reference only `elevation-1..3`.

## 8. Iconography

One icon set: **Lucide** (`lucide-react`, version pinned in `package.json`; outline style, 24 px grid). No other icon source, no emoji, no filled icons (fill is used on chips, not glyphs). All icons are imported through one module `src/ui/icons.ts` that maps a Warden concept to a Lucide component, so a renamed Lucide icon is fixed in one place and components never import `lucide-react` directly (lint rule `no-restricted-imports`).

Sizes and stroke: 16 px with stroke 1.75 in rows and cards; 14 px with stroke 2 in chips; 20 px with stroke 1.5 in panel headers and empty states. Icons inherit `currentColor`. Decorative icons (next to a text label) have `aria-hidden="true"`; icon-only buttons have an `aria-label` from B07 and a tooltip.

| Concept | Lucide icon | Concept | Lucide icon |
|---|---|---|---|
| Effect allow | `shield-check` | Effect deny | `ban` |
| Effect approval_required / waiting for approval | `hand` | Untrusted observation | `triangle-alert` |
| Tier T0 local | `laptop` | Tier T1 company-hosted | `server` |
| Tier T2 company cloud | `cloud-cog` | Tier T3 vendor API | `globe` |
| Tier T4 subscription harness | `plug` | Harness (provider card) | `plug` |
| Sandbox L1 | `box` | Sandbox L2 | `container` |
| Sandbox violation | `shield-alert` | Egress / proxy | `network` |
| Chain verified / unverified | `link` | Chain failed | `link-2-off` |
| Cost (USD) | `coins` | Quota units | `ticket` |
| Budget | `wallet` | Model / model call | `cpu` |
| Routing decision | `route` | Fallback | `corner-down-right` |
| Tool `fs.read` | `file-text` | Tool `fs.list` | `folder-open` |
| Tool `fs.search` | `search` | Tool `fs.write` | `file-pen` |
| Tool `fs.patch` | `file-diff` | Tool `proc.exec` | `terminal` |
| Tool `git.status` / `git.diff` | `git-compare` | Tool `git.commit` | `git-commit-horizontal` |
| Tool `git.push` (host) | `upload` | Session branch | `git-branch` |
| Gate (G1, G2) | `milestone` | Plan | `list-checks` |
| Request (job) | `clipboard-list` | Verification / test report | `flask-conical` |
| Repair | `wrench` | Package install | `package` |
| Explain | `info` | Waiting for input / clarifying question | `circle-help` |
| Cancel running task | `circle-stop` | Resume from last gate | `play` |
| Iterate | `repeat` | Revoke grant | `undo-2` |
| Secret access / keychain | `key-round` | Redaction | `eye-off` |
| Classification public | `book-open` | Classification internal | `building-2` |
| Classification confidential | `lock` | Workspace | `folder-git-2` |
| Doctor | `stethoscope` | Audit / event log | `scroll-text` |
| Export | `download` | Settings | `settings` |
| Task queued | `circle-dashed` | Task running | `loader-circle` (static: `circle-dot`) |
| Task succeeded | `circle-check` | Task failed | `circle-x` |
| Task timed out | `timer-off` | Task cancelled | `circle-slash` |
| Task skipped / blocked | `circle-minus` | Doctor warning | `circle-alert` |
| Disclosure (collapsed / expanded) | `chevron-right` (rotates 90°) | Copy id | `copy` |

Names follow the current Lucide naming (post-2024 renames such as `circle-check`, `triangle-alert`, `loader-circle`). If the pinned version exports a name only under a newer alias, `icons.ts` imports the alias; the concept column is what components reference (`<Icon concept="effect.deny" />`).

## 9. Motion

### 9.1 Tokens

| Token | Duration | Easing | Use |
|---|---|---|---|
| `motion-fast` | 100 ms | `cubic-bezier(0.2, 0, 0, 1)` (standard) | Hover and press colour, chevron rotation, focus ring appearance, chip state changes |
| `motion-base` | 180 ms | enter `cubic-bezier(0, 0, 0.2, 1)`, exit `cubic-bezier(0.4, 0, 1, 1)` | New timeline entry fade-in, context panel content swap, popover and listbox open/close, drawer |
| `motion-slow` | 280 ms | enter easing as above | Dialog open, ExplainDrawer slide, SetupWizard step change |

Motion tokens are CSS custom properties (`--motion-fast: 100ms`, `--ease-standard`, `--ease-enter`, `--ease-exit`; the easing names are NEW) and are not part of the JSON (the target tool has no motion category).

### 9.2 What animates and what does not

| Element | Animation | Rationale |
|---|---|---|
| New timeline entry | Opacity 0 → 1 over `motion-base`; **no** slide, no height animation | Insertion must not move content the user is reading (B05); virtualised rows are measured once |
| Streaming model text (`stream.delta`) | None; text is appended in place; a static block cursor `▍` in `color-text-muted` marks the live end and is removed on `model.call.end` | No blinking caret (SC 2.2.2); rendering budget in B08 §12 |
| Step counter, elapsed time, cost | Digits change in place with tabular figures; no counting animation | Numbers are evidence, not decoration |
| Tool call rows collapsing / expanding | Chevron rotates (`motion-fast`); content appears instantly with opacity `motion-fast` | Height animation inside a virtual list causes re-measure jank |
| Running task | `loader-circle` rotates 360° per 1000 ms, linear, infinite | The only continuous animation; it is small (16 px) and pausable via reduced motion |
| Waiting for approval | Pulse ring, §9.3 | WRD-11 §2.3 "pulsing state" |
| ApprovalCard appearance | Opacity + 4 px upward translate over `motion-base`. Focus never moves to the card automatically; the arrival is announced politely ("Press G then A to review", B05 §12.4, B07 `live.approval_needed`) and the pulse (§9.3) and PendingApprovalBadge mark it | Draws the eye once without moving other content (the card occupies reserved space) or stealing focus from what the user is doing (core §15 ID-15) |
| Context panel swap | Cross-fade `motion-base` | |
| Dialogs, drawers | Opacity + 8 px translate (drawer) or scale 0.98 → 1 (dialog), `motion-slow` | |
| Theme switch, density switch | None | |
| Banners (ST-2, ST-4, ST-6) | None; they appear in reserved header space | Error surfaces must be stable |

### 9.3 Waiting pulse

The icon of a task in `waiting_for_approval` (and the PendingApprovalBadge) is surrounded by a ring in `color-state-waiting`: `box-shadow: 0 0 0 0 var(--color-state-waiting)` animated to a 6 px spread with opacity 0.55 → 0, duration 1600 ms, `ease-out`, **3 iterations**, then the animation stops and a static 2 px ring in `color-state-waiting` remains. The pulse restarts (3 iterations) when a new approval enters that task and when the window regains focus while approvals are pending. Limiting it to 4.8 s satisfies SC 2.2.2 (no automatic movement lasting more than 5 s without a pause control) while still drawing attention; persistence of the signal is carried by the static ring, the "Waiting for approval" label, the badge count and the OS notification (B05).

### 9.4 Reduced motion

Applied when `prefers-reduced-motion: reduce` is set by the OS, or when the in-app setting "Reduce motion" (System / On / Off, NEW; default System) is On. Implementation: a `data-motion="reduced"` attribute on `<html>` that the CSS below keys on; B08 sets it from `matchMedia` plus the setting.

| Normal | Reduced replacement |
|---|---|
| Rotating `loader-circle` | Static `circle-dot` icon; the "Running · step n/max" label and the step counter carry progress |
| Waiting pulse ring | Static 2 px ring from the start; no animation |
| Fade-in of new entries, cross-fades | Instant (duration 0) |
| Translate and scale on cards, dialogs, drawers | Opacity only, `motion-fast` (opacity changes are not vestibular triggers) |
| Chevron rotation | Instant swap between `chevron-right` and `chevron-down` |
| Smooth scrolling (`G` then `A` jump to pending approval, `J`/`K`) | `scroll-behavior: auto` (jump) |

## 10. Contrast and preference media queries

- `@media (prefers-contrast: more)`: `color-border` takes the value of `color-border-strong`, `color-text-muted` takes the value of `color-text`, and tint backgrounds (`*-bg`) keep their values but gain a 1 px solid border in their foreground colour. (ASM: macOS "Increase contrast" and GNOME high-contrast set this media feature in the WebKit webviews Tauri uses.)
- `@media (forced-colors: active)` is not a target (Windows is out of scope), but chips use real borders rather than background-only shapes, so they survive forced colours if the app is ever run there.

## 11. Implementation (CSS custom properties)

Tokens are emitted by a build step (`apps/desktop/scripts/tokens-to-css.ts`, NEW) from the JSON in §12 into `src/ui/tokens.css`. Shape of the output:

```css
:root, [data-theme="light"] {
  --color-bg: #F6F7F9; --color-surface-1: #FFFFFF; /* … every color token, light value … */
  --elevation-1: 0 1px 2px 0 #0F172A14; /* light shadows */
  --space-1: 4px; /* … */ --radius-sm: 4px; /* … */
  --motion-fast: 100ms; --motion-base: 180ms; --motion-slow: 280ms;
  --ease-standard: cubic-bezier(0.2, 0, 0, 1); --ease-enter: cubic-bezier(0, 0, 0.2, 1); --ease-exit: cubic-bezier(0.4, 0, 1, 1);
  color-scheme: light;
}
[data-theme="dark"] {
  --color-bg: #0D1117; --color-surface-1: #161B22; /* … every color token, dark value … */
  --elevation-1: 0 0 0 1px #FFFFFF0F; /* value of elevation-1-dark; same for 2 and 3 */
  color-scheme: dark;
}
@media (prefers-color-scheme: dark) { :root:not([data-theme="light"]) { /* same as [data-theme="dark"] */ } }
[data-motion="reduced"] { --motion-fast: 0ms; --motion-base: 0ms; --motion-slow: 0ms; }
```

`data-theme` is absent when the setting is System (the media query decides) and set to `light` or `dark` when the user picks one. `color-scheme` makes native scrollbars and form controls follow the theme. Components use only `var(--…)`; a stylelint rule rejects raw hex values outside `tokens.css`.

## 12. Token set (JSON for the design-system tool)

Exact shape requested for import. Colour values are hex only; every name appears once and matches `[A-Za-z0-9][A-Za-z0-9_.-]*`; every token has a usage note. Motion tokens are in §9 (no motion category in this format). This JSON is the single source for §11's CSS and for the contrast check script.

```json
{"name":"Warden","version":1,
"color":{"themes":[{"id":"light","name":"Light"},{"id":"dark","name":"Dark"}],"tokens":[
 {"name": "color-bg", "value": {"light": "#F6F7F9", "dark": "#0D1117"}, "usage": "App background behind panels (window chrome, gutters between panels)."},
 {"name": "color-surface-1", "value": {"light": "#FFFFFF", "dark": "#161B22"}, "usage": "Primary panel and card surface: timeline, context panel, cards, dialogs."},
 {"name": "color-surface-2", "value": {"light": "#F0F2F5", "dark": "#1D232C"}, "usage": "Secondary surface: hovered row, section headers, input fields, internal-classification chip fill, code gutters."},
 {"name": "color-surface-3", "value": {"light": "#E4E8ED", "dark": "#262D37"}, "usage": "Tertiary surface: pressed or selected-inactive row, skeleton blocks, kbd hint background."},
 {"name": "color-border", "value": {"light": "#D8DDE3", "dark": "#2D3440"}, "usage": "Decorative dividers and card outlines where the edge is not the only cue (no contrast requirement)."},
 {"name": "color-border-strong", "value": {"light": "#7D8898", "dark": "#6E7A8B"}, "usage": "Boundaries of interactive controls (inputs, checkboxes, scope selector segments, outline chips); meets 3:1."},
 {"name": "color-text", "value": {"light": "#1A1F26", "dark": "#E6EAF0"}, "usage": "Primary text and icons."},
 {"name": "color-text-muted", "value": {"light": "#56606D", "dark": "#9BA5B3"}, "usage": "Secondary text: timestamps, elapsed time, metadata, placeholder, disabled-reason lines."},
 {"name": "color-text-inverse", "value": {"light": "#FFFFFF", "dark": "#0D1117"}, "usage": "Text and icons on filled accent, confidential chip and filled state pills."},
 {"name": "color-focus", "value": {"light": "#1B62D6", "dark": "#79B0FF"}, "usage": "2 px focus ring (outline with 2 px offset) on every focusable element."},
 {"name": "color-accent", "value": {"light": "#1F5FC8", "dark": "#6EA8FE"}, "usage": "Primary button fill, links, selected timeline entry indicator bar, running progress."},
 {"name": "color-accent-hover", "value": {"light": "#194FA8", "dark": "#9AC2FF"}, "usage": "Primary button hover and pressed fill."},
 {"name": "color-accent-subtle", "value": {"light": "#E8F0FC", "dark": "#1A2A42"}, "usage": "Selected timeline row background, selected file in diff file list."},
 {"name": "color-effect-allow", "value": {"light": "#1A7432", "dark": "#56D364"}, "usage": "Effect allow: icon and text in policy.decision rows, allow chips."},
 {"name": "color-effect-allow-bg", "value": {"light": "#EAF6EC", "dark": "#15291C"}, "usage": "Tint behind allow chips and granted-approval rows."},
 {"name": "color-effect-deny", "value": {"light": "#B3261E", "dark": "#FF8A80"}, "usage": "Effect deny: icon and text in denied tool call rows, proxy.denied, deny chips."},
 {"name": "color-effect-deny-bg", "value": {"light": "#FCEDEB", "dark": "#3A1A1A"}, "usage": "Tint behind deny chips and denied rows; ST-6 banner background."},
 {"name": "color-effect-approval", "value": {"light": "#8F5200", "dark": "#F0B64D"}, "usage": "Effect approval_required: ApprovalCard stripe, pending badge, approval chips; also waiting state."},
 {"name": "color-effect-approval-bg", "value": {"light": "#FFF4E0", "dark": "#33260F"}, "usage": "ApprovalCard background, pending-approval badge fill tint."},
 {"name": "color-untrusted", "value": {"light": "#6E3FAF", "dark": "#C9A8FF"}, "usage": "Untrusted-data marker on observations (file content, command output, harness output) and taint notes."},
 {"name": "color-untrusted-bg", "value": {"light": "#F3EDFB", "dark": "#2A2140"}, "usage": "Tint behind untrusted-output blocks in the context panel."},
 {"name": "color-tier-t0", "value": {"light": "#08403B", "dark": "#9BEBDF"}, "usage": "Tier T0 local: chip text, icon, border. Darkest in light theme, brightest in dark theme (most inside)."},
 {"name": "color-tier-t1", "value": {"light": "#0B554F", "dark": "#78D6C9"}, "usage": "Tier T1 company-hosted: chip text, icon, border."},
 {"name": "color-tier-t2", "value": {"light": "#0E6760", "dark": "#58BFB2"}, "usage": "Tier T2 company cloud tenant: chip text, icon, border."},
 {"name": "color-tier-t3", "value": {"light": "#5A647D", "dark": "#9CA3B8"}, "usage": "Tier T3 vendor API: outline chip text, icon, dashed border (outside the company)."},
 {"name": "color-tier-t4", "value": {"light": "#666E84", "dark": "#868DA2"}, "usage": "Tier T4 subscription harness: outline chip text, icon, dashed border (outside, least control)."},
 {"name": "color-tier-inside-bg", "value": {"light": "#E3F3F0", "dark": "#10302C"}, "usage": "Fill of T0 to T2 tier chips and of the 'Inside the company' group header in ModelPicker."},
 {"name": "color-state-running", "value": {"light": "#1F5FC8", "dark": "#6EA8FE"}, "usage": "Task state running: spinner, step counter, left stripe."},
 {"name": "color-state-waiting", "value": {"light": "#8F5200", "dark": "#F0B64D"}, "usage": "Task states waiting_for_approval and waiting_for_input: icon, pulse ring, stripe."},
 {"name": "color-state-succeeded", "value": {"light": "#1A7432", "dark": "#56D364"}, "usage": "Task state succeeded; doctor check ok; chain verified."},
 {"name": "color-state-failed", "value": {"light": "#B3261E", "dark": "#FF8A80"}, "usage": "Task states failed and timed_out; doctor check fail; chain failed."},
 {"name": "color-state-cancelled", "value": {"light": "#5F6773", "dark": "#9BA5B3"}, "usage": "Task state cancelled, skipped, blocked; greyed entries after ST-5."},
 {"name": "color-diff-add", "value": {"light": "#E6F4EA", "dark": "#12261A"}, "usage": "Background of added lines in DiffViewer."},
 {"name": "color-diff-del", "value": {"light": "#FCECEA", "dark": "#2E1616"}, "usage": "Background of deleted lines in DiffViewer."},
 {"name": "color-diff-add-strong", "value": {"light": "#1A7432", "dark": "#56D364"}, "usage": "Plus sign in the add gutter and the add count in file stats."},
 {"name": "color-diff-del-strong", "value": {"light": "#B3261E", "dark": "#FF8A80"}, "usage": "Minus sign in the delete gutter and the delete count in file stats."},
 {"name": "color-diff-add-word", "value": {"light": "#C4E7CD", "dark": "#1E4A2B"}, "usage": "Intra-line changed-word highlight on added lines."},
 {"name": "color-diff-del-word", "value": {"light": "#F6CBC6", "dark": "#5A2525"}, "usage": "Intra-line changed-word highlight on deleted lines."},
 {"name": "color-class-public", "value": {"light": "#4E5866", "dark": "#A7B0BD"}, "usage": "Classification public: outline chip text, icon and border."},
 {"name": "color-class-internal", "value": {"light": "#1C4E9E", "dark": "#8DB8FF"}, "usage": "Classification internal: chip text and icon on surface-2 fill with solid border."},
 {"name": "color-class-confidential", "value": {"light": "#8A1C55", "dark": "#F2A7CF"}, "usage": "Classification confidential: solid chip fill (text-inverse on top) and lock icon elsewhere."},
 {"name": "color-scrim", "value": {"light": "#0D111766", "dark": "#000000A3"}, "usage": "Backdrop behind confirmation dialogs (not behind approvals, which are inline)."}
]},
"type":{"fonts":[],"families":{"sans": "\"Inter\", system-ui, -apple-system, \"Segoe UI\", Roboto, \"Helvetica Neue\", Arial, sans-serif", "mono": "\"JetBrains Mono\", ui-monospace, \"SF Mono\", Menlo, Consolas, \"Liberation Mono\", monospace"},"groups":[
 {"name":"Text","family":"sans","styles":[
  {"name": "text-xs", "fontSize": "11px", "lineHeight": "16px", "fontWeight": 500, "usage": "Badges, chips, kbd hints, counters. Never below 11 px."},
  {"name": "text-sm", "fontSize": "12px", "lineHeight": "16px", "fontWeight": 400, "usage": "Metadata: timestamps, elapsed time, cost badges, routing line secondary part, table cells in dense lists."},
  {"name": "text-sm-strong", "fontSize": "12px", "lineHeight": "16px", "fontWeight": 600, "usage": "Column headers, chip labels needing emphasis, group headings in ModelPicker."},
  {"name": "text-md", "fontSize": "13px", "lineHeight": "20px", "fontWeight": 400, "usage": "Default UI text: timeline entry titles, card body, form fields, buttons."},
  {"name": "text-md-strong", "fontSize": "13px", "lineHeight": "20px", "fontWeight": 600, "usage": "Card titles (TaskCard, ApprovalCard 'what' line label), button labels of primary actions."},
  {"name": "text-lg", "fontSize": "15px", "lineHeight": "22px", "fontWeight": 600, "usage": "Panel titles (context panel header, gate titles 'Review the plan', 'Review the result')."},
  {"name": "text-xl", "fontSize": "18px", "lineHeight": "24px", "fontWeight": 600, "usage": "Screen titles (Workspace home, Settings, Doctor and audit). One per screen."}
 ]},
 {"name":"Mono","family":"mono","styles":[
  {"name": "mono-sm", "fontSize": "12px", "lineHeight": "16px", "fontWeight": 400, "usage": "Ids (call_…, apr_…), rule ids, hashes, file paths in dense rows, argv in ToolCallRow."},
  {"name": "mono-md", "fontSize": "13px", "lineHeight": "20px", "fontWeight": 400, "usage": "Diff lines, command in the ApprovalCard 'what' line, plan JSON editor, test failure excerpts."}
 ]}
]},
"spacing":{"tokens":[
 {"name": "space-1", "value": "4px", "usage": "Icon-to-label gap inside chips; gap between stacked badges."},
 {"name": "space-2", "value": "8px", "usage": "Default gap between inline items; horizontal padding of chips and compact buttons; timeline row vertical padding (compact)."},
 {"name": "space-3", "value": "12px", "usage": "Horizontal padding of timeline rows and inputs; gap between card sections."},
 {"name": "space-4", "value": "16px", "usage": "Card padding; panel gutters; gap between cards in the timeline."},
 {"name": "space-5", "value": "20px", "usage": "Timeline indentation for nested entries (tool calls under a task card)."},
 {"name": "space-6", "value": "24px", "usage": "Panel padding at 1440 px and above; dialog padding."},
 {"name": "space-7", "value": "32px", "usage": "Separation between page-level sections (Settings groups, Doctor groups)."},
 {"name": "space-8", "value": "48px", "usage": "Empty-state vertical padding; top offset of centered setup content."}
]},
"radius":{"tokens":[
 {"name": "radius-sm", "value": "4px", "usage": "Chips, badges, kbd hints, inputs, compact buttons, code blocks."},
 {"name": "radius-md", "value": "6px", "usage": "Cards (TaskCard, ApprovalCard, PlanCard), buttons, menus, popovers."},
 {"name": "radius-lg", "value": "10px", "usage": "Dialogs, drawers (ExplainDrawer edge), Banner, SetupWizard panel."},
 {"name": "radius-pill", "value": "999px", "usage": "PendingApprovalBadge count and the running-step counter pill (NEW)."}
]},
"shadow":{"tokens":[
 {"name": "elevation-1", "value": "0 1px 2px 0 #0F172A14", "usage": "Light theme: resting cards on color-bg (TaskCard, PlanCard, ApprovalCard). Dark theme uses elevation-1-dark."},
 {"name": "elevation-2", "value": "0 4px 12px -2px #0F172A1F, 0 2px 4px -2px #0F172A14", "usage": "Light theme: menus, popovers, ModelPicker listbox, tooltips, sticky GateBar."},
 {"name": "elevation-3", "value": "0 16px 40px -8px #0F172A33, 0 4px 12px -4px #0F172A1F", "usage": "Light theme: confirmation dialogs and ExplainDrawer."},
 {"name": "elevation-1-dark", "value": "0 0 0 1px #FFFFFF0F", "usage": "Dark theme resting cards: hairline highlight; depth comes from color-surface-1 on color-bg (NEW)."},
 {"name": "elevation-2-dark", "value": "0 0 0 1px #FFFFFF14, 0 8px 20px -4px #00000099", "usage": "Dark theme menus and popovers on color-surface-2 (NEW)."},
 {"name": "elevation-3-dark", "value": "0 0 0 1px #FFFFFF1A, 0 20px 48px -8px #000000B3", "usage": "Dark theme dialogs and drawers on color-surface-3 (NEW)."}
]}
}
```

## Traceability

| Design element | WRD source (doc §) | Requirement / invariant satisfied |
|---|---|---|
| Light and dark token sets, OS-following theme | WRD-16 §13 design constraints | "Light and dark themes" |
| Effect encodings allow / deny / approval (§3.1) | WRD-08 §3, WRD-11 §2.3, WRD-16 §13 screen 4 | Decisions visible before effects (WRD-11 §1.1); BI-1 made visible in the timeline |
| Untrusted-output block (§3.1) | WRD-16 §7.3, WRD-10 T-03 | BI-4 visible in the UI |
| Ordered tier encoding, inside/outside grouping (§3.2) | WRD-06 §3, WRD-16 §6.3, §3 step 7 | BI-7 visible ("Anthropic and Copilot turn grey with the reason"); tier order T0 to T4 |
| ModelPicker inadmissible rows at full contrast with reason | WRD-16 §13 screen 2, §15 item 8 | BI-7; WCAG 1.4.3 (no opacity dimming) |
| Classification chips with lightness order (§3.4) | WRD-16 §2.3, §13 screen 1; CF-02 | Three PoC classifications; `confidential` emphasised |
| Task-state encodings incl. `skipped`, `blocked` (§3.3) | WRD-16 §8, WRD-07 §4; CF-27 | Eleven states distinguishable without colour |
| Chain status encodings (§3.5) | WRD-16 §13 screens 5 and 7; WRD-11 §3 | ST-6 recognisable; H5 evidence visible |
| Diff colours with sign gutter and underline (§3.6) | WRD-16 §13 screen 5 | Diff readable in greyscale |
| Contrast verification of 89 pairs (§4) | WRD-01 N-7; brief §5 B06 | WCAG 2.2 AA 1.4.3, 1.4.11 in both themes |
| Tabular figures for live numbers (§5.1) | WRD-11 §1.3, WRD-16 §13 screen 5 cost panel | Streaming without layout jumps (B05) |
| Mono for ids and rule ids (§5.2) | WRD-11 §5 | "Policy denials include the rule id for support" |
| Latin Extended-A subset | WRD-11 §6 | Romanian and German localisation later |
| Compact density, 28 px rows, 24 px minimum targets (§6.2) | WRD-16 §13 screen 2; WRD-01 N-7 | Dense timeline; WCAG 2.5.8 |
| Sticky-bar scroll padding | WRD-16 §13 screens 3, 5 | WCAG 2.4.11 focus not obscured |
| Focus ring spec (§7) | WRD-11 §6, WRD-01 N-7 | Keyboard-first approvals; WCAG 2.4.7 |
| Single icon set with concept map (§8) | WRD-16 §13 design constraints | Consistent, tone "developer tool" |
| Waiting pulse limited to 4.8 s, static ring after (§9.3) | WRD-11 §2.3 | "Pulsing state" plus WCAG 2.2.2 |
| ApprovalCard entrance without focus move (§9.2) | core §15 ID-15; B05 §12.4 | Incoming events never move focus (WCAG 3.2.2 predictability); arrival announced politely |
| Reduced-motion replacements (§9.4) | WRD-11 §6 | "Reduced-motion mode" |
| No motion on streaming text, no slide on insertion | WRD-01 N-2, WRD-16 §13 | First token visible promptly; no layout jumps |
| JSON token set and generated CSS (§11, §12) | brief §5 B06 | One source of truth for tokens |

## Deviations and assumptions

- NEW colour tokens: `color-accent-hover`, `color-accent-subtle`, `color-effect-allow-bg`, `color-effect-deny-bg`, `color-effect-approval-bg`, `color-untrusted-bg`, `color-tier-inside-bg`, `color-diff-add-strong`, `color-diff-del-strong`, `color-diff-add-word`, `color-diff-del-word`, `color-scrim`. Needed for tinted cards, tier chip fills, diff gutters and dialog backdrops; core §11 names only the foreground roles.
- NEW type styles `text-sm-strong`, `text-md-strong` (weight variants; the JSON format has one weight per style).
- NEW radius `radius-pill`; NEW shadow tokens `elevation-1-dark`, `elevation-2-dark`, `elevation-3-dark` (the JSON format has no per-theme shadow value, so the dark values are separate tokens and the CSS maps `--elevation-N` per theme).
- NEW motion easing variables `--ease-standard`, `--ease-enter`, `--ease-exit`; NEW user settings `density` (compact/comfortable), `reduce_motion` (system/on/off) and theme (system/light/dark), stored locally by the UI (B08), not in the daemon.
- NEW build files `apps/desktop/scripts/check-contrast.py` and `apps/desktop/scripts/tokens-to-css.ts` (A17 CI job should run the contrast check; ASM that A17 accepts a UI lint step).
- DEV: WRD-11 §2.3 asks for a "pulsing state" without limit; the pulse is limited to three cycles (4.8 s) followed by a static ring to satisfy WCAG 2.2.2. The waiting state stays visible through ring, label, badge and OS notification.
- DEV: WRD-11 §2.4 per-file accept/revert has no visual treatment here (CF-37: no per-file revert in the PoC).
- ASM: `text-md` is 13 px (developer-tool density, as in common IDEs) rather than 14 px; with Inter's large x-height this remains legible, and 200 % zoom is supported. If the H6 session (B09) records legibility complaints, raising `text-md` to 14/20 is a token-only change.
- ASM: Inter 4.x and JetBrains Mono 2.3 are bundled under the SIL Open Font License 1.1 (both OFL; attribution in the About dialog).
- ASM: Lucide icon names are those of `lucide-react` releases from 2025 onward; `icons.ts` absorbs later renames.
- ASM: the tier hue families (teal inside, slate outside) are supporting cues; B04's `StatusBadge variant="tier"` must render both the icon and the text (long or short form with `aria-label`), never a colour swatch alone.
- ASM: WebKitGTK (Linux) and WKWebView (macOS) both honour `prefers-color-scheme`, `prefers-reduced-motion` and `prefers-contrast`; if WebKitGTK does not expose `prefers-contrast` on a given distribution, the in-app theme setting is the fallback.
