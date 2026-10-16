package execproto

// Limits of the exec.fs methods: design A06 §4.2 (the methods table), the
// fs.json schema of §4.3 (the minimum, maximum, maxLength and maxItems
// values) and §4.4 too_large. Both sides of the protocol use them: the
// executor enforces them on every request (Decode checks no ranges), and the
// daemon can refuse an out-of-range call before sending it. TestLimits pins
// each value to the schema kept under testdata/schema.
//
// Lengths the schema gives in characters (maxLength) are enforced here in
// bytes, which is never more permissive: a string of n characters has at
// least n bytes.
const (
	// MaxReadBytes is exec.fs.read's per-call cap: ReadParams.length
	// maximum and default (2 MiB).
	MaxReadBytes = 2 << 20

	// MaxWriteBytes is exec.fs.write's cap on the decoded content (4 MiB,
	// WriteParams.content; A06 §4.4 too_large). The patch method holds
	// each file it produces to the same cap.
	MaxWriteBytes = 4 << 20

	// DefaultListDepth and MaxListDepth bound ListParams.depth; 1 lists the
	// directory's own entries.
	DefaultListDepth = 1
	MaxListDepth     = 4

	// DefaultListEntries and MaxListEntries bound ListParams.max_entries.
	DefaultListEntries = 500
	MaxListEntries     = 2000

	// MaxSearchQueryBytes is SearchParams.query maxLength.
	MaxSearchQueryBytes = 1024

	// MaxSearchMatches is SearchParams.max_matches maximum and default, and
	// SearchResult.matches maxItems.
	MaxSearchMatches = 200

	// MaxSearchContextLines is SearchParams.context_lines maximum.
	MaxSearchContextLines = 3

	// MaxSearchFileBytes is SearchParams.max_file_bytes maximum and
	// default: a larger file is skipped (skipped.too_large), never read in
	// part.
	MaxSearchFileBytes = 2 << 20

	// MaxSearchGlobs and MaxSearchGlobBytes bound SearchParams.include and
	// exclude (maxItems, items maxLength).
	MaxSearchGlobs     = 32
	MaxSearchGlobBytes = 256

	// MaxSearchTextBytes is the maxLength of a match's text and of each
	// context line; longer lines are cut on a UTF-8 boundary.
	MaxSearchTextBytes = 512

	// MaxPatchBytes is PatchParams.patch maxLength (4 MiB).
	MaxPatchBytes = 4 << 20

	// DefaultPatchStrip and MaxPatchStrip bound PatchParams.strip.
	DefaultPatchStrip = 1
	MaxPatchStrip     = 2

	// MaxPatchFiles is the most files one patch may touch. PatchResult.files
	// has no maxItems in A06, but Decode refuses arrays longer than
	// MaxArrayLen, so a larger patch would be applied and its result then
	// refused by the daemon: the audit would record a failure for writes
	// that happened. The executor refuses such a patch before applying
	// anything (docs/DECISIONS-poc.md D-048).
	MaxPatchFiles = MaxArrayLen
)
