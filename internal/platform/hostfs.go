package platform

// HostFS answers the questions the L2 container spec (internal/sandbox/oci,
// design A06 §15, CONFLICTS C-03) has to ask about host directories before
// it binds them into a container. They are OS questions, so they live here
// (CLAUDE.md §3); the spec builder only sees the interface and its tests use
// a fake.
//
// Why each question exists: the worktree masks of A06 §11.3 are bind mounts
// keyed on one spelling of a path. On a host file system that resolves
// other spellings to the same entry, a sandboxed process reaches the masked
// file under a different name and the mask never applies (PROGRESS M2-12,
// CWE-178 and CWE-41):
//   - case: on NTFS (Windows, through Docker Desktop's file sharing) and on
//     APFS (macOS) ".ENV" opens the file masked as ".env";
//   - NTFS 8.3 short names: "ENV~1" opens it as well (D-055);
//   - a junction or symbolic link in a bind source makes Docker bind
//     something other than the path the checks examined.
type HostFS interface {
	// CaseSensitiveDir reports whether name lookups directly inside dir
	// distinguish case. On Windows it is the per-directory flag that
	// `fsutil file setCaseSensitiveInfo <dir> enable` sets; on macOS the
	// volume's case sensitivity; on Linux false only for a casefolded
	// directory or a file system that may fold case.
	CaseSensitiveDir(dir string) (bool, error)
	// ShortNameCreation reports whether the volume holding dir may create
	// NTFS 8.3 short names for new entries. Always false off Windows.
	ShortNameCreation(dir string) (bool, error)
	// HasShortAlias reports whether the entry at p has an 8.3 short name
	// that differs from its long name (created before creation was
	// disabled, for example). Always false off Windows.
	HasShortAlias(p string) (bool, error)
	// FinalPath returns the path the OS opens for p after resolving every
	// junction, symbolic link and short name, in the on-disk spelling. A
	// bind source is accepted only when FinalPath returns it unchanged.
	FinalPath(p string) (string, error)
}

// NewHostFS returns the HostFS of this OS.
func NewHostFS() HostFS { return hostFS{} }

// hostFS is the real implementation; its methods are per OS
// (hostfs_windows.go, hostfs_linux.go, hostfs_darwin.go, hostfs_other.go).
type hostFS struct{}
