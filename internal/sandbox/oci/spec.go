package oci

import (
	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/sandbox"
)

// Host operating systems, as platform.OS() names them. They decide the
// form of bind sources (MapHostPath) and which engine kind is possible.
const (
	HostLinux   = "linux"
	HostWindows = "windows"
	HostDarwin  = "darwin"
)

// Engine is the kind of Docker engine the Plan is for (M2-17 checks which
// one is running before a Plan is used).
type Engine string

// Engines.
const (
	// EngineRootless is rootless Docker or Podman on a Linux host: the
	// container's uid 0 is the host user, without capabilities (A06 §15).
	EngineRootless Engine = "rootless"
	// EngineDesktop is Docker Desktop (Windows, macOS, or Linux with
	// Desktop): a rootful engine inside a VM, so the container runs as a
	// non-root user (C-03).
	EngineDesktop Engine = "desktop"
)

// Options are the inputs of Build that do not come from the Spec.
type Options struct {
	HostOS       string // platform.OS()
	Engine       Engine
	SandboxImage string // repository@sha256:<64 hex>, from config.yaml sandbox.l2.images
	ProxyImage   string // repository@sha256:<64 hex>
	FS           platform.HostFS
}

// Artefact is a file the backend writes before it creates the container.
// Build never writes it: the bytes and the host path are returned so the
// launch step (M2-21) writes them, and tests compare them.
type Artefact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  []byte `json:"-"`
}

// Bind is a bind mount into a container.
type Bind struct {
	Kind sandbox.MountKind `json:"kind"`
	// Source is the host path in the form the engine accepts
	// (MapHostPath), or, with EngineSource, a path of the engine's own
	// Linux file system (the /dev/null that covers a masked file).
	Source       string `json:"source"`
	EngineSource bool   `json:"engine_source,omitempty"`
	Target       string `json:"target"`
	ReadOnly     bool   `json:"read_only"`
}

// Tmpfs is a tmpfs mount; Options is the mount option string
// (docker run --tmpfs <target>:<options>).
type Tmpfs struct {
	Kind    string `json:"kind"` // "tmp", "hooks_dir" or "mask"
	Target  string `json:"target"`
	Options string `json:"options"`
}

// Ulimit is one --ulimit name=soft:hard.
type Ulimit struct {
	Name string `json:"name"`
	Soft int64  `json:"soft"`
	Hard int64  `json:"hard"`
}

// Resources are the A06 §12 L2 limits.
type Resources struct {
	MemoryBytes     int64    `json:"memory_bytes"`
	MemorySwapBytes int64    `json:"memory_swap_bytes"`
	NanoCPUs        int64    `json:"nano_cpus"`
	PidsLimit       int64    `json:"pids_limit"`
	Ulimits         []Ulimit `json:"ulimits"`
}

// Attachment connects a container to a network under the given aliases.
type Attachment struct {
	Network string   `json:"network"`
	Aliases []string `json:"aliases,omitempty"`
}

// Network is a per-task Docker network (bridge driver).
type Network struct {
	Name     string            `json:"name"`
	Internal bool              `json:"internal"`
	Labels   map[string]string `json:"labels"`
}

// Container is one container's configuration, in our own terms; M2-21
// maps it onto the Engine API's container config and host config.
type Container struct {
	Name       string            `json:"name"`
	Hostname   string            `json:"hostname"`
	Image      string            `json:"image"`
	User       string            `json:"user"`
	Entrypoint []string          `json:"entrypoint"`
	Cmd        []string          `json:"cmd"`
	Env        []string          `json:"env"`
	WorkingDir string            `json:"working_dir"`
	Labels     map[string]string `json:"labels"`

	ReadonlyRootfs  bool     `json:"readonly_rootfs"`
	Privileged      bool     `json:"privileged"`
	CapDrop         []string `json:"cap_drop"`
	NoNewPrivileges bool     `json:"no_new_privileges"`
	// Seccomp is the host path of the profile artefact (Plan.Seccomp);
	// the Engine API takes its bytes inline as "seccomp=<json>".
	Seccomp string `json:"seccomp,omitempty"`

	Resources Resources    `json:"resources"`
	Tmpfs     []Tmpfs      `json:"tmpfs"`
	Binds     []Bind       `json:"binds"`
	Networks  []Attachment `json:"networks"`

	OpenStdin  bool `json:"open_stdin"`
	StdinOnce  bool `json:"stdin_once"`
	AutoRemove bool `json:"auto_remove"`
}

// Plan is everything M2-21 creates for one L2 sandbox, in creation order:
// the networks, the sidecar, then the sandbox.
type Plan struct {
	Internal Network   `json:"internal_network"`
	Egress   Network   `json:"egress_network"`
	Sidecar  Container `json:"sidecar"`
	Sandbox  Container `json:"sandbox"`
	Seccomp  Artefact  `json:"seccomp_profile"`
}
