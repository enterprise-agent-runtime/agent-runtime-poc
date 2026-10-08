package platform

import (
	"encoding/binary"
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

	cases := []struct {
		name string
		sd   []byte
		sid  []byte
		want bool
	}{
		{"owner only", testSD(seDACLPresent|seDACLProtected, allowed(user)), user, true},
		{"RID 500 owner", testSD(seDACLPresent|seDACLProtected, allowed(admin)), admin, true},
		{"not protected", testSD(seDACLPresent, allowed(user)), user, false},
		{"DACL absent", testSD(seDACLProtected), user, false}, // offset 0; the present bit is guarded below

		{"NULL DACL", nullDACLSD(), user, false},
		{"two ACEs", testSD(seDACLPresent|seDACLProtected, allowed(user), allowed(user)), user, false},
		{"no ACE", testSD(seDACLPresent | seDACLProtected), user, false},
		{"deny ACE", testSD(seDACLPresent|seDACLProtected, testACE(1, 0, fileAllAccess, user)), user, false},
		{"inherited ACE", testSD(seDACLPresent|seDACLProtected, testACE(0, 0x10, fileAllAccess, user)), user, false},
		{"wrong mask", testSD(seDACLPresent|seDACLProtected, testACE(0, 0, 0x120089, user)), user, false},
		{"different SID", testSD(seDACLPresent|seDACLProtected, allowed(other)), user, false},
		{"SID prefix only", testSD(seDACLPresent|seDACLProtected, allowed(user)), user[:len(user)-4], false},
		{"empty SID", testSD(seDACLPresent|seDACLProtected, allowed(user)), nil, false},
		{"empty SD", nil, user, false},
	}
	for _, c := range cases {
		if got := ownerOnlyDACL(c.sd, c.sid); got != c.want {
			t.Errorf("%s: ownerOnlyDACL = %v, want %v", c.name, got, c.want)
		}
	}

	// Truncated buffers and lying offsets or lengths are rejected, never a
	// panic: the bytes come from the OS, but a parser that trusts them is a
	// parser that can be crashed.
	good := testSD(seDACLPresent|seDACLProtected, allowed(user))
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
	} {
		if ownerOnlyDACL(b, user) {
			t.Errorf("%s: accepted", name)
		}
	}
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
// seDACLPresent is set, holds the given ACEs and directly follows the header.
func testSD(control uint16, aces ...[]byte) []byte {
	b := make([]byte, sdHeaderSize)
	b[0] = 1
	binary.LittleEndian.PutUint16(b[2:], control|seSelfRelative)
	if control&seDACLPresent == 0 {
		return b
	}
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
	return b
}

// nullDACLSD is "DACL present" with a zero offset: a NULL DACL, which grants
// everyone everything. The explicit `off == 0` check in ownerOnlyDACL is
// defence in depth: deleting it leaves this case rejected anyway (read at
// offset 0, the "ACE" SID overlaps OffsetDacl, which is 0, so validSID
// fails), so no test can kill that mutant. Checked by hand in review of FX-3.
func nullDACLSD() []byte {
	b := make([]byte, sdHeaderSize)
	b[0] = 1
	binary.LittleEndian.PutUint16(b[2:], seDACLPresent|seDACLProtected|seSelfRelative)
	return b
}
