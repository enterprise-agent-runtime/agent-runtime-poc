//go:build linux

package linux_bwrap

import (
	"bytes"
	"io"
	"testing"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/sandbox/seccomp"
)

// TestSeccompFile_HoldsTheSealedProgram: bwrap reads the A06 §11.5
// program from fd 5 at its current offset. The memfd must hold exactly
// Render(arch), start at offset 0, and be sealed, so nothing that shares
// the descriptor (it is inherited by bwrap) can change the filter between
// Render and the moment bwrap installs it. It is close-on-exec in the
// daemon; ExtraFiles dup2s it to fd 5 for bwrap only.
func TestSeccompFile_HoldsTheSealedProgram(t *testing.T) {
	for _, a := range seccomp.Arches() {
		t.Run(a.String(), func(t *testing.T) {
			want, err := seccomp.Render(a)
			if err != nil {
				t.Fatal(err)
			}
			f, err := SeccompFile(a)
			if err != nil {
				t.Fatalf("SeccompFile() = %v", err)
			}
			defer f.Close()
			got, err := io.ReadAll(f)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("memfd holds %d bytes, want the %d rendered bytes", len(got), len(want))
			}
			seals, err := unix.FcntlInt(f.Fd(), unix.F_GET_SEALS, 0)
			if err != nil {
				t.Fatalf("F_GET_SEALS: %v (not a sealable memfd)", err)
			}
			for name, s := range map[string]int{"SEAL": unix.F_SEAL_SEAL, "SHRINK": unix.F_SEAL_SHRINK, "GROW": unix.F_SEAL_GROW, "WRITE": unix.F_SEAL_WRITE} {
				if seals&s == 0 {
					t.Errorf("F_SEAL_%s not set (seals %#x)", name, seals)
				}
			}
			if _, err := f.WriteAt([]byte{0}, 0); err == nil {
				t.Error("write to the sealed program succeeded")
			}
			if err := f.Truncate(0); err == nil {
				t.Error("truncate of the sealed program succeeded")
			}
			flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFD, 0)
			if err != nil || flags&unix.FD_CLOEXEC == 0 {
				t.Errorf("F_GETFD = %#x, %v; want FD_CLOEXEC", flags, err)
			}
			// A second reader of the same descriptor (bwrap after dup2)
			// starts at offset 0: SeccompFile leaves it there.
			g, err := SeccompFile(a)
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			if off, err := g.Seek(0, io.SeekCurrent); err != nil || off != 0 {
				t.Errorf("offset = %d, %v; want 0", off, err)
			}
		})
	}
}
