package execproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"
)

// Protocol is the wire protocol version (A06 §3.1 --protocol, exec.hello).
const Protocol = "warden.exec/1"

// MaxFrameBytes is the largest Content-Length frame; larger frames close the
// channel (A06 §4.1).
const MaxFrameBytes = 8 << 20

// Method names (A06 §4.2). WRD-04 §5 writes the git family as exec.git.*;
// A06 defines the single method exec.git.run, which is what is implemented.
const (
	MethodHello    = "exec.hello"
	MethodFSRead   = "exec.fs.read"
	MethodFSList   = "exec.fs.list"
	MethodFSSearch = "exec.fs.search"
	MethodFSWrite  = "exec.fs.write"
	MethodFSPatch  = "exec.fs.patch"
	MethodFSStat   = "exec.fs.stat"
	MethodSpawn    = "exec.proc.spawn"
	MethodIO       = "exec.proc.io" // notification, executor → daemon
	MethodWait     = "exec.proc.wait"
	MethodSignal   = "exec.proc.signal"
	MethodGitRun   = "exec.git.run"
	MethodShutdown = "exec.shutdown"
)

// Decode unmarshals one JSON value into v. It rejects, at every depth:
// unknown fields (A06 §4.3: additionalProperties false throughout), keys
// that match a field only case-insensitively, duplicate keys, and trailing
// data. encoding/json alone accepts the last two kinds and folds case, so a
// message could mean one thing to the schema or the policy engine and
// another to the executor (CWE-436; see checkKeys). Range and required
// checks are not done here.
func Decode(data []byte, v any) error {
	if err := checkKeys(data, reflect.TypeOf(v)); err != nil {
		return fmt.Errorf("execproto: decode: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("execproto: decode: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("execproto: decode: trailing data after JSON value")
	}
	return nil
}

// ---- common.json ----

// RootName names a sandbox root (common.json RootName).
type RootName string

// The roots of A06 §2 and §3.1 --roots.
const (
	RootWorktree RootName = "worktree"
	RootScratch  RootName = "scratch"
	RootCache    RootName = "cache"
	RootGit      RootName = "git"
)

// Encoding of content and data strings (common.json Encoding).
type Encoding string

// Encodings.
const (
	EncodingUTF8   Encoding = "utf-8"
	EncodingBase64 Encoding = "base64"
)

// Kind is a file system entry kind (common.json Kind).
type Kind string

// Kinds.
const (
	KindFile    Kind = "file"
	KindDir     Kind = "dir"
	KindSymlink Kind = "symlink"
	KindOther   Kind = "other"
)

// Resolved is the result of path resolution inside the sandbox (A06 §5.1).
// RequestedRelPath is optional in the schema but always sent: its zero value
// "" is the root itself, which omitempty would lose.
type Resolved struct {
	Root             RootName `json:"root"`
	RelPath          string   `json:"rel_path"`
	Canonical        string   `json:"canonical"`
	RequestedRelPath string   `json:"requested_rel_path"`
	DenyMatch        *string  `json:"deny_match"` // null when no deny-list glob matched
}

// ViolationKind is the sandbox.violation kind of an error (A06 §4.4); ""
// stands for the schema's null (no violation).
type ViolationKind string

// Violation kinds.
const (
	ViolationPathEscape ViolationKind = "path_escape"
	ViolationDenyList   ViolationKind = "deny_list"
	ViolationRootCheck  ViolationKind = "root_check"
)

// ErrorData is JSON-RPC error.data for every executor error (common.json
// ErrorData). Code is the name of the numeric error code (Code.Name).
type ErrorData struct {
	Code      string        `json:"code"`
	Violation bool          `json:"violation"`
	Kind      ViolationKind `json:"kind,omitempty"`
	Detail    string        `json:"detail,omitempty"`
	RelPath   string        `json:"rel_path,omitempty"`
}

// ---- hello.json ----

// Mode is the executor mode (A06 §3.1 --mode).
type Mode string

// Modes.
const (
	ModeTool    Mode = "tool"
	ModeHarness Mode = "harness"
)

// RootMode is a root's access mode.
type RootMode string

// Root modes.
const (
	RootRW RootMode = "rw"
	RootRO RootMode = "ro"
)

// HelloParams is exec.hello params.
type HelloParams struct {
	Protocol      string `json:"protocol"`
	SandboxID     string `json:"sandbox_id"`
	SessionID     string `json:"session_id"`
	DaemonVersion string `json:"daemon_version"`
}

// HelloRoot is one entry of HelloResult.Roots.
type HelloRoot struct {
	Name string   `json:"name"`
	Path string   `json:"path"`
	Mode RootMode `json:"mode"`
}

// HelloFeatures reports kernel features the executor found. All four are
// always sent: false is information here, not absence.
type HelloFeatures struct {
	OpenAt2      bool `json:"openat2"`
	ONofollowAny bool `json:"o_nofollow_any"`
	PIDNamespace bool `json:"pid_namespace"`
	Cgroup       bool `json:"cgroup"`
}

// HelloResult is exec.hello result. The daemon compares Roots, Mode and
// ProfilesDigest with its launch spec (A06 §4.3).
type HelloResult struct {
	Protocol        string        `json:"protocol"`
	ExecutorVersion string        `json:"executor_version"`
	OS              string        `json:"os"`
	Arch            string        `json:"arch"`
	Mode            Mode          `json:"mode"`
	Roots           []HelloRoot   `json:"roots"`
	ProfilesDigest  string        `json:"profiles_digest"`
	Features        HelloFeatures `json:"features"`
	PID             int           `json:"pid"`
	UID             int           `json:"uid"`
	ProxyListen     *string       `json:"proxy_listen,omitempty"`
}

// ---- fs.json ----

// ReadParams is exec.fs.read params. Zero Offset and Length mean the
// defaults (0 and 2 MiB).
type ReadParams struct {
	Path            string   `json:"path"`
	Root            RootName `json:"root,omitempty"`
	Offset          int64    `json:"offset,omitempty"`
	Length          int64    `json:"length,omitempty"`
	ExpectCanonical string   `json:"expect_canonical,omitempty"`
}

// ReadResult is exec.fs.read result.
type ReadResult struct {
	Resolved Resolved `json:"resolved"`
	Content  string   `json:"content"`
	Encoding Encoding `json:"encoding"`
	Size     int64    `json:"size"`
	Offset   int64    `json:"offset"`
	Returned int64    `json:"returned"`
	EOF      bool     `json:"eof"`
	Sha256   string   `json:"sha256"`
}

// ListParams is exec.fs.list params.
type ListParams struct {
	Path       string   `json:"path,omitempty"`
	Root       RootName `json:"root,omitempty"`
	Depth      int      `json:"depth,omitempty"`
	MaxEntries int      `json:"max_entries,omitempty"`
}

// ListEntry is one directory entry. Size is a pointer because 0 is a real
// size; denied entries omit Size and MTime.
type ListEntry struct {
	RelPath    string     `json:"rel_path"`
	Kind       Kind       `json:"kind"`
	Size       *int64     `json:"size,omitempty"`
	Mode       string     `json:"mode,omitempty"`
	MTime      *time.Time `json:"mtime,omitempty"`
	LinkTarget string     `json:"link_target,omitempty"`
	Denied     bool       `json:"denied"`
}

// ListResult is exec.fs.list result.
type ListResult struct {
	Resolved  Resolved    `json:"resolved"`
	Entries   []ListEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

// SearchParams is exec.fs.search params. CaseSensitive defaults to true,
// so it is a pointer.
type SearchParams struct {
	Query         string   `json:"query"`
	Regex         bool     `json:"regex,omitempty"`
	CaseSensitive *bool    `json:"case_sensitive,omitempty"`
	Path          string   `json:"path,omitempty"`
	Root          RootName `json:"root,omitempty"`
	Include       []string `json:"include,omitempty"`
	Exclude       []string `json:"exclude,omitempty"`
	MaxMatches    int      `json:"max_matches,omitempty"`
	ContextLines  int      `json:"context_lines,omitempty"`
	MaxFileBytes  int64    `json:"max_file_bytes,omitempty"`
}

// SearchMatch is one match.
type SearchMatch struct {
	RelPath string   `json:"rel_path"`
	Line    int      `json:"line"`
	Column  int      `json:"column"`
	Text    string   `json:"text"`
	Before  []string `json:"before,omitempty"`
	After   []string `json:"after,omitempty"`
}

// SearchSkipped counts files not searched, by reason (always all four).
type SearchSkipped struct {
	Denied     int `json:"denied"`
	Binary     int `json:"binary"`
	TooLarge   int `json:"too_large"`
	Unreadable int `json:"unreadable"`
}

// SearchResult is exec.fs.search result.
type SearchResult struct {
	Matches      []SearchMatch `json:"matches"`
	FilesScanned int           `json:"files_scanned"`
	Truncated    bool          `json:"truncated"`
	Skipped      SearchSkipped `json:"skipped"`
}

// WriteMode is exec.fs.write mode.
type WriteMode string

// Write modes; "" means the default, overwrite.
const (
	WriteCreate    WriteMode = "create"
	WriteOverwrite WriteMode = "overwrite"
)

// WriteParams is exec.fs.write params (worktree only, A06 §5.3).
type WriteParams struct {
	Path            string    `json:"path"`
	Content         string    `json:"content"`
	Encoding        Encoding  `json:"encoding,omitempty"`
	Mode            WriteMode `json:"mode,omitempty"`
	Executable      bool      `json:"executable,omitempty"`
	ExpectCanonical string    `json:"expect_canonical,omitempty"`
}

// WriteResult is exec.fs.write result.
type WriteResult struct {
	Resolved     Resolved `json:"resolved"`
	BytesWritten int64    `json:"bytes_written"`
	Created      bool     `json:"created"`
	Sha256       string   `json:"sha256"`
}

// PatchParams is exec.fs.patch params. Strip defaults to 1 and 0 is valid,
// so it is a pointer.
type PatchParams struct {
	Patch  string `json:"patch"`
	Strip  *int   `json:"strip,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// PatchOp is what a patch did to one file.
type PatchOp string

// Patch operations.
const (
	PatchModify PatchOp = "modify"
	PatchCreate PatchOp = "create"
	PatchDelete PatchOp = "delete"
	PatchRename PatchOp = "rename"
)

// PatchFile is one file of a patch result.
type PatchFile struct {
	RelPath     string  `json:"rel_path"`
	FromRelPath string  `json:"from_rel_path,omitempty"`
	Op          PatchOp `json:"op"`
	Hunks       int     `json:"hunks"`
	Sha256After string  `json:"sha256_after,omitempty"`
}

// PatchResult is exec.fs.patch result.
type PatchResult struct {
	Applied bool        `json:"applied"`
	Files   []PatchFile `json:"files"`
}

// StatParams is exec.fs.stat params, the pre-PDP canonicalization step
// (A06 §5.4).
type StatParams struct {
	Path string   `json:"path"`
	Root RootName `json:"root,omitempty"`
}

// StatResult is exec.fs.stat result.
type StatResult struct {
	Resolved Resolved   `json:"resolved"`
	Exists   bool       `json:"exists"`
	Kind     Kind       `json:"kind,omitempty"`
	Size     *int64     `json:"size,omitempty"`
	Mode     string     `json:"mode,omitempty"`
	MTime    *time.Time `json:"mtime,omitempty"`
}

// ---- proc.json ----

// AuthzKind says who asked for a spawn.
type AuthzKind string

// Authz kinds.
const (
	AuthzTool     AuthzKind = "tool"
	AuthzVerifier AuthzKind = "verifier"
	AuthzHarness  AuthzKind = "harness"
)

// Authz carries the PDP decision a spawn executes under (A06 §6 step 5).
// Profile is required and "" when the PDP matched no profile.
type Authz struct {
	DecisionID string    `json:"decision_id"`
	Profile    string    `json:"profile"`
	Approved   bool      `json:"approved"`
	Kind       AuthzKind `json:"kind"`
}

// SpawnParams is exec.proc.spawn params. It carries a secret (SecretEnv);
// json.Marshal and slog redact it, and only EncodeSpawnParams / MarshalWire
// put the value on the wire (secret.go).
type SpawnParams struct {
	CallID         string    `json:"call_id"`
	Argv           []string  `json:"argv"`
	Cwd            string    `json:"cwd,omitempty"`
	TimeoutMS      int64     `json:"timeout_ms"`
	MaxOutputBytes int64     `json:"max_output_bytes,omitempty"`
	SecretEnv      SecretEnv `json:"secret_env,omitzero"`
	Authz          Authz     `json:"authz"`
}

// SpawnResult is exec.proc.spawn result. Profile is the executor's own
// match ("" when none).
type SpawnResult struct {
	Handle     string `json:"handle"`
	PID        int    `json:"pid"`
	PGID       int    `json:"pgid"`
	Executable string `json:"executable"`
	Profile    string `json:"profile"`
}

// Stream is the stream an output chunk belongs to.
type Stream string

// Streams.
const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
	StreamMarker Stream = "marker"
)

// Segment says whether a chunk precedes or follows the truncation marker
// (A06 §7.5).
type Segment string

// Segments.
const (
	SegmentHead Segment = "head"
	SegmentTail Segment = "tail"
)

// IONotification is the exec.proc.io notification.
type IONotification struct {
	Handle   string   `json:"handle"`
	CallID   string   `json:"call_id"`
	Seq      int64    `json:"seq"`
	Stream   Stream   `json:"stream"`
	Data     string   `json:"data"`
	Encoding Encoding `json:"encoding"`
	Segment  Segment  `json:"segment"`
}

// WaitParams is exec.proc.wait params; TimeoutMS 0 waits until exit.
type WaitParams struct {
	Handle    string `json:"handle"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

// WaitState is a handle's state.
type WaitState string

// Wait states.
const (
	StateRunning WaitState = "running"
	StateExited  WaitState = "exited"
)

// WaitReason classifies an exit (A06 §7.4).
type WaitReason string

// Wait reasons.
const (
	ReasonExit             WaitReason = "exit"
	ReasonSignal           WaitReason = "signal"
	ReasonTimeout          WaitReason = "timeout"
	ReasonCancelled        WaitReason = "cancelled"
	ReasonResource         WaitReason = "resource"
	ReasonExecutorShutdown WaitReason = "executor_shutdown"
)

// ResourceKind names the exhausted resource.
type ResourceKind string

// Resource kinds.
const (
	ResourceMemory  ResourceKind = "memory"
	ResourcePids    ResourceKind = "pids"
	ResourceCPU     ResourceKind = "cpu"
	ResourceFsize   ResourceKind = "fsize"
	ResourceTmpFull ResourceKind = "tmp_full"
	ResourceDisk    ResourceKind = "disk"
)

// WaitResource details a resource exit.
type WaitResource struct {
	Kind   ResourceKind `json:"kind,omitempty"`
	Detail string       `json:"detail,omitempty"`
}

// WaitBytes counts output bytes (always all four).
type WaitBytes struct {
	Stdout    int64 `json:"stdout"`
	Stderr    int64 `json:"stderr"`
	Forwarded int64 `json:"forwarded"`
	Discarded int64 `json:"discarded"`
}

// Rusage is the child's resource usage.
type Rusage struct {
	UserMS   int64 `json:"user_ms"`
	SysMS    int64 `json:"sys_ms"`
	MaxRSSKB int64 `json:"max_rss_kb"`
}

// WaitResult is exec.proc.wait result. ExitCode and Signal are the schema's
// integer|null and string|null: nil is sent as absent.
type WaitResult struct {
	Handle       string        `json:"handle"`
	State        WaitState     `json:"state"`
	ExitCode     *int          `json:"exit_code,omitempty"`
	Signal       *string       `json:"signal,omitempty"`
	Reason       WaitReason    `json:"reason,omitempty"`
	Resource     *WaitResource `json:"resource,omitempty"`
	SeccompKill  bool          `json:"seccomp_kill,omitempty"`
	DurationMS   int64         `json:"duration_ms,omitempty"`
	Bytes        *WaitBytes    `json:"bytes,omitempty"`
	Truncated    bool          `json:"truncated,omitempty"`
	StraysKilled int           `json:"strays_killed,omitempty"`
	Rusage       *Rusage       `json:"rusage,omitempty"`
}

// Signal is a signal the daemon may send to a handle's process group.
type Signal string

// Signals.
const (
	SignalINT  Signal = "INT"
	SignalTERM Signal = "TERM"
	SignalKILL Signal = "KILL"
)

// SignalParams is exec.proc.signal params.
type SignalParams struct {
	Handle string `json:"handle"`
	Signal Signal `json:"signal"`
}

// SignalResult is exec.proc.signal result.
type SignalResult struct {
	Delivered bool `json:"delivered"`
	PGID      int  `json:"pgid"`
}

// ShutdownReason says why the executor is told to stop.
type ShutdownReason string

// Shutdown reasons.
const (
	ShutdownTaskEnd    ShutdownReason = "task_end"
	ShutdownCancel     ShutdownReason = "cancel"
	ShutdownTimeout    ShutdownReason = "timeout"
	ShutdownResource   ShutdownReason = "resource"
	ShutdownDaemonStop ShutdownReason = "daemon_stop"
	ShutdownError      ShutdownReason = "error"
)

// ShutdownParams is exec.shutdown params. GraceMS defaults to 3000 and 0 is
// valid, so it is a pointer.
type ShutdownParams struct {
	GraceMS *int           `json:"grace_ms,omitempty"`
	Reason  ShutdownReason `json:"reason,omitempty"`
}

// ShutdownResult is exec.shutdown result.
type ShutdownResult struct {
	KilledPIDs int `json:"killed_pids"`
	Remaining  int `json:"remaining"`
}

// ---- git.json ----

// GitOp is a restricted git operation (A14 §6).
type GitOp string

// Git operations.
const (
	GitStatus GitOp = "status"
	GitDiff   GitOp = "diff"
	GitCommit GitOp = "commit"
	GitHead   GitOp = "head"
)

// GitDiffOptions are the diff options. ContextLines defaults to 3 and 0 is
// valid, so it is a pointer; Against "" means "base".
type GitDiffOptions struct {
	Against      string   `json:"against,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	StatOnly     bool     `json:"stat_only,omitempty"`
	ContextLines *int     `json:"context_lines,omitempty"`
}

// GitCommitOptions are the commit options.
type GitCommitOptions struct {
	Message string   `json:"message"`
	Paths   []string `json:"paths,omitempty"`
}

// GitRunParams is exec.git.run params.
type GitRunParams struct {
	Op        GitOp             `json:"op"`
	Diff      *GitDiffOptions   `json:"diff,omitempty"`
	Commit    *GitCommitOptions `json:"commit,omitempty"`
	TimeoutMS int64             `json:"timeout_ms,omitempty"`
}

// GitHeadInfo is the head object of a git result.
type GitHeadInfo struct {
	Commit string `json:"commit,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// GitRunResult is exec.git.run result.
type GitRunResult struct {
	ExitCode  int          `json:"exit_code"`
	Stdout    string       `json:"stdout"`
	Stderr    string       `json:"stderr"`
	Encoding  Encoding     `json:"encoding"`
	Truncated bool         `json:"truncated"`
	Head      *GitHeadInfo `json:"head,omitempty"`
}
