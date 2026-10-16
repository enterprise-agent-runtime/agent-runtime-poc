package platform

import (
	"encoding/binary"
	"os"
	"testing"
)

// TestOwnerOnlyDACL guards the structural owner-only check (CLAUDE.md §4 IPC,
// docs/DECISIONS-poc.md D-011). It exists because OwnerOnly used to compare
// the DACL as SDDL text, and Windows abbreviates well-known SIDs when it
// renders SDDL: on GitHub's windows-latest runner (runneradmin, RID 500) the
// read-back "D:P(A;;FA;;;LA)" never equalled the written "...;;;S-1-5-21-…-500)"
// and TestWriteOwnerOnly failed although the DACL was right. The parser is
// pure, so every accept/reject case is checked here on every OS.
func TestOwnerOnlyDACL(t *testing.T) {
	user := testSID(5, 21, 1111, 2222, 3333, 1001)
	admin := testSID(5, 21, 1111, 2222, 3333, 500) // RID 500, rendered "LA"
	other := testSID(5, 21, 1111, 2222, 3333, 1002)
	allowed := func(sid []byte) []byte { return testACE(0, 0, fileAllAccess, sid) }
	// An ACE whose AceSize covers four bytes of padding after the SID: the
	// SID is compared by its declared length, not to the end of the ACE.
	padded := append(allowed(user), 0, 0, 0, 0)
	binary.LittleEndian.PutUint16(padded[2:], uint16(len(padded)))

	cases := []struct {
		name string
		sd   []byte
		sid  []byte
		want bool
	}{
		{"owner only", testSD(user, seDACLPresent|seDACLProtected, allowed(user)), user, true},
		{"RID 500 owner", testSD(admin, seDACLPresent|seDACLProtected, allowed(admin)), admin, true},
		{"not protected", testSD(user, seDACLPresent, allowed(user)), user, false},
		{"DACL absent", testSD(user, seDACLProtected), user, false}, // offset 0; the present bit is guarded below

		{"NULL DACL", nullDACLSD(user), user, false},
		{"two ACEs", testSD(user, seDACLPresent|seDACLProtected, allowed(user), allowed(user)), user, false},
		{"no ACE", testSD(user, seDACLPresent|seDACLProtected), user, false},
		{"deny ACE", testSD(user, seDACLPresent|seDACLProtected, testACE(1, 0, fileAllAccess, user)), user, false},
		{"inherited ACE", testSD(user, seDACLPresent|seDACLProtected, testACE(0, 0x10, fileAllAccess, user)), user, false},
		{"wrong mask", testSD(user, seDACLPresent|seDACLProtected, testACE(0, 0, 0x120089, user)), user, false},
		{"different SID", testSD(user, seDACLPresent|seDACLProtected, allowed(other)), user, false},
		{"SID prefix only", testSD(user, seDACLPresent|seDACLProtected, allowed(user)), user[:len(user)-4], false},
		{"empty SID", testSD(user, seDACLPresent|seDACLProtected, allowed(user)), nil, false},
		{"empty SD", nil, user, false},

		// Added by the mutation audit of FX-3; each row kills a mutant the
		// rows above let through.
		{"ACE padded after SID", testSD(user, seDACLPresent|seDACLProtected, padded), user, true},                                  // SID compared to the ACE's end
		{"inherit-only ACE", testSD(user, seDACLPresent|seDACLProtected, testACE(0, 0x08, fileAllAccess, user)), user, false},      // grants nothing on the file itself; kills a flags check narrowed to INHERITED_ACE
		{"DACL inside the header", headerOverlapSD(user), user, false},                                                             // review of 2fab6a3: offset 10, the ACL reuses header bytes
		{"mask superset", testSD(user, seDACLPresent|seDACLProtected, testACE(0, 0, fileAllAccess|0x10000000, user)), user, false}, // WriteOwnerOnly writes exactly FA; kills mask&FA == FA

		// Security review of FX-3 (WH-1+2, CWE-283): the owner of a file holds
		// READ_CONTROL and WRITE_DAC whatever its DACL says, so a token file
		// owned by anyone but the user (another user who pre-created it, or
		// BUILTIN\Administrators, an elevated token's default owner) is not
		// owner-only although its DACL is.
		{"owner is a different SID", testSD(other, seDACLPresent|seDACLProtected, allowed(user)), user, false},
		{"owner is Administrators", testSD(testSID(5, 32, 544), seDACLPresent|seDACLProtected, allowed(user)), user, false},
		{"no owner", testSD(nil, seDACLPresent|seDACLProtected, allowed(user)), user, false},
		{"owner is a SID prefix", testSD(user[:len(user)-4], seDACLPresent|seDACLProtected, allowed(user)), user, false},
		{"directory ACE on a file", testSD(user, seDACLPresent|seDACLProtected, testACE(0, objectInheritACE|containerInheritACE, fileAllAccess, user)), user, false},
	}
	for _, c := range cases {
		if got := ownerOnlyDACL(c.sd, c.sid); got != c.want {
			t.Errorf("%s: ownerOnlyDACL = %v, want %v", c.name, got, c.want)
		}
	}

	// Truncated buffers and lying offsets or lengths are rejected, never a
	// panic: the bytes come from the OS, but a parser that trusts them is a
	// parser that can be crashed.
	good := testSD(user, seDACLPresent|seDACLProtected, allowed(user))
	for n := 0; n < len(good); n++ {
		if ownerOnlyDACL(good[:n], user) {
			t.Errorf("truncated to %d bytes: accepted", n)
		}
	}
	lie := func(off int, v uint32, size int) []byte {
		b := append([]byte(nil), good...)
		if size == 2 {
			binary.LittleEndian.PutUint16(b[off:], uint16(v))
		} else {
			binary.LittleEndian.PutUint32(b[off:], v)
		}
		return b
	}
	acl := sdHeaderSize
	ace := acl + aclHeaderSize
	aclEnd := ace + len(allowed(user)) // the owner SID starts here
	for name, b := range map[string][]byte{
		"DACL offset past end":   lie(16, uint32(len(good)), 4),
		"DACL offset huge":       lie(16, 0xFFFFFFF0, 4),
		"AclSize past end":       lie(acl+2, 0xFFFF, 2),
		"AclSize below header":   lie(acl+2, 4, 2),
		"AceSize past ACL":       lie(ace+2, 0xFFFF, 2),
		"AceSize below header":   lie(ace+2, 4, 2),
		"SID subauth count lies": lie(ace+8, 0x0F01, 2), // revision 1, 15 subauthorities
		"not self-relative":      lie(2, seDACLPresent|seDACLProtected, 2),
		// A valid owner-only ACL at a real offset, but SE_DACL_PRESENT clear:
		// Windows then treats the descriptor as having no DACL, which grants
		// everyone full access. "DACL absent" above has offset 0 and so is
		// rejected by the offset check first; this case alone guards the
		// present-bit check (review of 0c98a32).
		"present bit clear, valid ACL": lie(2, seDACLProtected|seSelfRelative, 2),
		// Mutation audit of FX-3. AceCount 0 with a valid ACE in the bytes is
		// an empty DACL to Windows (no access for anyone), not owner-only;
		// kills "AceCount > 1". An AclSize leaving a partial ACE header kills
		// the removal of the ACE header length check (it panics instead). An
		// AceSize that ends inside the SID must not be read past; kills the
		// removal of ace = ace[:aceSize].
		"AceCount 0, ACE present":     lie(acl+4, 0, 2),
		"AclSize cuts ACE header":     lie(acl+2, aclHeaderSize+2, 2),
		"AceSize ends inside the SID": lie(ace+2, uint32(aclEnd-ace-4), 2),
		// Security review of FX-3 (WH-1+2): the owner offset is bounds-checked
		// like the DACL's. 0 is "no owner"; an offset inside the header would
		// read the SID out of the header's own fields; one inside the DACL
		// reads the ACL header, which is no SID; past the end must not panic.
		"owner offset 0":           lie(4, 0, 4),
		"owner offset in header":   lie(4, 8, 4),
		"owner offset at the DACL": lie(4, uint32(acl), 4),
		"owner offset past end":    lie(4, uint32(len(good)), 4),
		"owner offset huge":        lie(4, 0xFFFFFFF0, 4),
		"owner subauth count lies": lie(aclEnd, 0x0F01, 2), // revision 1, 15 subauthorities
	} {
		if ownerOnlyDACL(b, user) {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestValidSID pins the SID shape check the owner-only parser relies on for
// both the caller's SID and the one in the ACE (mutation audit of FX-3:
// dropping the revision, subauthority or length check left TestOwnerOnlyDACL
// green, because a mismatched SID is rejected by the final comparison too).
func TestValidSID(t *testing.T) {
	user := testSID(5, 21, 1111, 2222, 3333, 1001)
	rev := func(r byte) []byte { b := append([]byte(nil), user...); b[0] = r; return b }
	cases := []struct {
		name string
		b    []byte
		want bool
	}{
		{"user SID", user, true},
		{"trailing bytes", append(append([]byte(nil), user...), 0, 0, 0, 0), true}, // ACE padding
		{"one subauthority", testSID(5, 18), true},                                 // S-1-5-18, the shortest real SID shape
		{"revision 0", rev(0), false},
		{"revision 2", rev(2), false},
		{"no subauthorities", testSID(5), false},
		{"one byte short", user[:len(user)-1], false},
		{"header only", user[:sidHeaderSize], false},
		{"shorter than header", user[:sidHeaderSize-1], false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := validSID(c.b); got != c.want {
			t.Errorf("%s: validSID = %v, want %v", c.name, got, c.want)
		}
	}
}

// FuzzOwnerOnlyDACL: the security descriptor comes from the OS, but the
// parser must hold for any bytes. Property: it never panics, and it never
// accepts unless the descriptor is self-relative with a present, protected
// DACL and contains the exact owner-only ACE body (type 0, flags 0, mask
// FILE_ALL_ACCESS, followed by the caller's SID), and its owner offset, past
// the header, points at the caller's SID (WH-1+2). The seeds run as part of
// every go test; explore further with
// go test -run '^$' -fuzz FuzzOwnerOnlyDACL ./internal/platform/
func FuzzOwnerOnlyDACL(f *testing.F) {
	user := testSID(5, 21, 1111, 2222, 3333, 1001)
	good := testSD(user, seDACLPresent|seDACLProtected, testACE(0, 0, fileAllAccess, user))
	f.Add(good, user)
	f.Add(good[:len(good)-1], user)
	f.Add(testSD(user, seDACLPresent|seDACLProtected, testACE(0, 0, fileAllAccess, user), testACE(0, 0, fileAllAccess, user)), user)
	f.Add(testSD(user, seDACLPresent, testACE(0, 0, fileAllAccess, user)), user)
	f.Add(nullDACLSD(user), user)
	f.Add([]byte{}, []byte{})
	f.Add(headerOverlapSD(user), user)                                                                          // review of 2fab6a3
	f.Add(testSD(testSID(5, 32, 544), seDACLPresent|seDACLProtected, testACE(0, 0, fileAllAccess, user)), user) // WH-1+2: foreign owner
	f.Add(testSD(nil, seDACLPresent|seDACLProtected, testACE(0, 0, fileAllAccess, user)), user)                 // WH-1+2: no owner
	f.Fuzz(func(t *testing.T, sd, sid []byte) {
		if !ownerOnlyDACL(sd, sid) {
			return
		}
		control := binary.LittleEndian.Uint16(sd[2:])
		if control&(seSelfRelative|seDACLPresent|seDACLProtected) != seSelfRelative|seDACLPresent|seDACLProtected {
			t.Fatalf("accepted with control %#04x", control)
		}
		// Follow the parser: the DACL starts after the header, and its first
		// ACE is ACCESS_ALLOWED without flags, with body FA || sid.
		off := uint64(binary.LittleEndian.Uint32(sd[16:]))
		ace := off + aclHeaderSize
		body := binary.LittleEndian.AppendUint32(nil, fileAllAccess)
		body = append(body, sid...)
		if off < sdHeaderSize || ace+aceHeaderSize+uint64(len(body)) > uint64(len(sd)) {
			t.Fatalf("accepted with DACL offset %d outside the descriptor body: sd %x", off, sd)
		}
		if sd[ace] != accessAllowedACEType || sd[ace+1] != 0 || string(sd[ace+aceHeaderSize:ace+aceHeaderSize+uint64(len(body))]) != string(body) {
			t.Fatalf("accepted without an owner-only ACE for the SID at offset %d: sd %x sid %x", ace, sd, sid)
		}
		// WH-1+2: and the owner, past the header, is exactly the SID.
		owner := uint64(binary.LittleEndian.Uint32(sd[4:]))
		if owner < sdHeaderSize || owner+uint64(len(sid)) > uint64(len(sd)) || string(sd[owner:owner+uint64(len(sid))]) != string(sid) {
			t.Fatalf("accepted with owner offset %d not holding the SID: sd %x sid %x", owner, sd, sid)
		}
	})
}

// testSID builds a binary SID S-1-<auth>-<sub...>.
func testSID(auth byte, subs ...uint32) []byte {
	b := []byte{1, byte(len(subs)), 0, 0, 0, 0, 0, auth}
	for _, s := range subs {
		b = binary.LittleEndian.AppendUint32(b, s)
	}
	return b
}

// testACE builds an ACE: header (type, flags, size), mask, SID.
func testACE(typ, flags byte, mask uint32, sid []byte) []byte {
	b := []byte{typ, flags}
	b = binary.LittleEndian.AppendUint16(b, uint16(8+len(sid)))
	b = binary.LittleEndian.AppendUint32(b, mask)
	return append(b, sid...)
}

// testSD builds a self-relative security descriptor whose DACL, when
// seDACLPresent is set, holds the given ACEs and directly follows the
// header; the owner SID, when not nil, follows the DACL.
func testSD(owner []byte, control uint16, aces ...[]byte) []byte {
	b := make([]byte, sdHeaderSize)
	b[0] = 1
	binary.LittleEndian.PutUint16(b[2:], control|seSelfRelative)
	if control&seDACLPresent != 0 {
		binary.LittleEndian.PutUint32(b[16:], sdHeaderSize)
		size := aclHeaderSize
		for _, a := range aces {
			size += len(a)
		}
		b = append(b, 2, 0)
		b = binary.LittleEndian.AppendUint16(b, uint16(size))
		b = binary.LittleEndian.AppendUint16(b, uint16(len(aces)))
		b = append(b, 0, 0)
		for _, a := range aces {
			b = append(b, a...)
		}
	}
	return withOwner(b, owner)
}

// withOwner appends owner to sd and points OffsetOwner at it (nil: no owner).
func withOwner(sd, owner []byte) []byte {
	if owner == nil {
		return sd
	}
	binary.LittleEndian.PutUint32(sd[4:], uint32(len(sd)))
	return append(sd, owner...)
}

// headerOverlapSD places the DACL at offset 10, inside the 20-byte header:
// the ACL's Sbz2 is the low half of OffsetDacl and the ACE's type and flags
// are its zero high bytes, so a parser that only bounds-checks the end of
// the buffer reads a well-formed owner-only ACE out of it. The kernel never
// returns such a descriptor; the parser must still refuse it. The owner is
// valid (after the ACE), so only the DACL offset can reject it.
func headerOverlapSD(sid []byte) []byte {
	const off = 10
	aceSize := aceHeaderSize + 4 + len(sid)
	b := make([]byte, off+aclHeaderSize+aceSize)
	b[0] = 1
	binary.LittleEndian.PutUint16(b[2:], seDACLPresent|seDACLProtected|seSelfRelative)
	b[off] = 2                                                              // AclRevision
	binary.LittleEndian.PutUint16(b[off+2:], uint16(aclHeaderSize+aceSize)) // AclSize
	binary.LittleEndian.PutUint16(b[off+4:], 1)                             // AceCount
	binary.LittleEndian.PutUint32(b[16:], off)                              // OffsetDacl; also Sbz2 and the ACE's type/flags
	ace := off + aclHeaderSize
	binary.LittleEndian.PutUint16(b[ace+2:], uint16(aceSize))
	binary.LittleEndian.PutUint32(b[ace+4:], fileAllAccess)
	copy(b[ace+8:], sid)
	return withOwner(b, sid)
}

// nullDACLSD is "DACL present" with a zero offset: a NULL DACL, which grants
// everyone everything. ownerOnlyDACL rejects it with `off < sdHeaderSize`,
// which since the review of 2fab6a3 covers offset 0 and every other offset
// inside the header; headerOverlapSD is the case that kills a mutant
// narrowing it back to `off == 0`. The owner is valid, so only the NULL DACL
// can reject it.
func nullDACLSD(owner []byte) []byte {
	b := make([]byte, sdHeaderSize)
	b[0] = 1
	binary.LittleEndian.PutUint16(b[2:], seDACLPresent|seDACLProtected|seSelfRelative)
	return withOwner(b, owner)
}

// TestOwnerOnlyDirDACL guards the directory form of the check (WH-1+2,
// security review of FX-3, CWE-276): Ensure() used to create the home and
// run directories with MkdirAll alone, whose mode means nothing on Windows,
// so they kept the DACL inherited from %LOCALAPPDATA%. They now carry a
// protected DACL whose one ACE is inherited by files and subdirectories
// (OI|CI), owned by the user; anything else is refused.
func TestOwnerOnlyDirDACL(t *testing.T) {
	user := testSID(5, 21, 1111, 2222, 3333, 1001)
	other := testSID(5, 21, 1111, 2222, 3333, 1002)
	dir := func(flags byte, sid []byte) []byte { return testACE(0, flags, fileAllAccess, sid) }
	const oici = objectInheritACE | containerInheritACE
	cases := []struct {
		name string
		sd   []byte
		want bool
	}{
		{"owner-only, OI|CI", testSD(user, seDACLPresent|seDACLProtected, dir(oici, user)), true},
		{"not protected", testSD(user, seDACLPresent, dir(oici, user)), false},
		{"file ACE (not inheritable)", testSD(user, seDACLPresent|seDACLProtected, dir(0, user)), false},
		{"inherited ACE", testSD(user, seDACLPresent|seDACLProtected, dir(oici|0x10, user)), false},
		{"inherit-only ACE", testSD(user, seDACLPresent|seDACLProtected, dir(oici|0x08, user)), false},
		{"other SID", testSD(user, seDACLPresent|seDACLProtected, dir(oici, other)), false},
		{"foreign owner", testSD(other, seDACLPresent|seDACLProtected, dir(oici, user)), false},
		{"extra ACE", testSD(user, seDACLPresent|seDACLProtected, dir(oici, user), dir(oici, other)), false},
	}
	for _, c := range cases {
		if got := ownerOnlyDirDACL(c.sd, user); got != c.want {
			t.Errorf("%s: ownerOnlyDirDACL = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestOwnerOnlyMode is the Unix owner-only decision, pure so the foreign
// owner case runs without root (WH-1+2, security review of FX-3, CWE-283):
// OwnerOnly used to check the permission bits only, so a 0600 token file
// owned by another uid (who can chmod it, and read it) passed.
func TestOwnerOnlyMode(t *testing.T) {
	cases := []struct {
		name       string
		perm       os.FileMode
		owner, uid int
		want       bool
	}{
		{"0600, own", 0o600, 1000, 1000, true},
		{"0400, own", 0o400, 1000, 1000, true},
		{"0600, other uid", 0o600, 1001, 1000, false},
		{"0600, root-owned", 0o600, 0, 1000, false},
		{"0640, own", 0o640, 1000, 1000, false},
		{"0604, own", 0o604, 1000, 1000, false},
	}
	for _, c := range cases {
		if got := ownerOnlyMode(c.perm, c.owner, c.uid); got != c.want {
			t.Errorf("%s: ownerOnlyMode = %v, want %v", c.name, got, c.want)
		}
	}
}
