// Package seccomp implements the Linux L1 system-call filter of design A06
// §11.5 (WRD-16 §10.3, WRD-10 §5.2) and, from the same rows, the L2 Docker
// seccomp profile of design A06 §15.
//
// The A06 §11.5 table lives here once, as Go data (Table). Two renderers read
// it:
//
//   - Program and Render build the classic-BPF program that bwrap installs
//     from --add-seccomp-fd (A06 §11.1): an array of struct sock_filter,
//     little-endian, one program per architecture (x86_64 and aarch64). The
//     assembler is written here in pure Go: no cgo, no libseccomp and no
//     golang.org/x/net/bpf (docs/DECISIONS-poc.md D-041).
//   - DockerProfile renders the JSON for docker run --security-opt seccomp=
//     (defaultAction SCMP_ACT_ALLOW, the listed syscalls with
//     SCMP_ACT_ERRNO or SCMP_ACT_KILL_PROCESS and their argument filters).
//
// Rendering is data, so the package builds and is tested on every OS. The
// kernel constants it needs are defined here with the kernel header they
// come from; on Linux, consts_linux*.go assert at compile time that they equal
// golang.org/x/sys/unix, so a wrong number fails the Linux build for that
// architecture.
//
// Version is the "seccomp" value of sandbox.create.limits (A06 §13.2). Change
// it whenever the rendered program or profile changes meaning.
package seccomp
