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

		// Added by the mutation audit of FX-3; each row kills a mutant the
		// rows above let through.
		{"ACE padded after SID", testSD(seDACLPresent|seDACLProtected, padded), user, true},                                  // SID compared to the ACE's end
		{"inherit-only ACE", testSD(seDACLPresent|seDACLProtected, testACE(0, 0x08, fileAllAccess, user)), user, false},      // grants nothing on the file itself; kills a flags check narrowed to INHERITED_ACE
		{"mask superset", testSD(seDACLPresent|seDACLProtected, testACE(0, 0, fileAllAccess|0x10000000, user)), user, false}, // WriteOwnerOnly writes exactly FA; kills mask&FA == FA
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
		// Mutation audit of FX-3. AceCount 0 with a valid ACE in the bytes is
		// an empty DACL to Windows (no access for anyone), not owner-only;
		// kills "AceCount > 1". An AclSize leaving a partial ACE header kills
		// the removal of the ACE header length check (it panics instead). An
		// AceSize that ends inside the SID must not be read past; kills the
		// removal of ace = ace[:aceSize].
		"AceCount 0, ACE present":     lie(acl+4, 0, 2),
		"AclSize cuts ACE header":     lie(acl+2, aclHeaderSize+2, 2),
		"AceSize ends inside the SID": lie(ace+2, uint32(len(good)-ace-4), 2),
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
// FILE_ALL_ACCESS, followed by the caller's SID). The seeds run as part of
// every go test; explore further with
// go test -run '^$' -fuzz FuzzOwnerOnlyDACL ./internal/platform/
func FuzzOwnerOnlyDACL(f *testing.F) {
	user := testSID(5, 21, 1111, 2222, 3333, 1001)
	good := testSD(seDACLPresent|seDACLProtected, testACE(0, 0, fileAllAccess, user))
	f.Add(good, user)
	f.Add(good[:len(good)-1], user)
	f.Add(testSD(seDACLPresent|seDACLProtected, testACE(0, 0, fileAllAccess, user), testACE(0, 0, fileAllAccess, user)), user)
	f.Add(testSD(seDACLPresent, testACE(0, 0, fileAllAccess, user)), user)
	f.Add(nullDACLSD(), user)
	f.Add([]byte{}, []byte{})
	f.Fuzz(func(t *testing.T, sd, sid []byte) {
		if !ownerOnlyDACL(sd, sid) {
			return
		}
		control := binary.LittleEndian.Uint16(sd[2:])
		if control&(seSelfRelative|seDACLPresent|seDACLProtected) != seSelfRelative|seDACLPresent|seDACLProtected {
			t.Fatalf("accepted with control %#04x", control)
		}
		body := binary.LittleEndian.AppendUint32(nil, fileAllAccess)
		body = append(body, sid...)
		for i := sdHeaderSize + aclHeaderSize + aceHeaderSize; i+len(body) <= len(sd); i++ {
			if sd[i-4] == accessAllowedACEType && sd[i-3] == 0 && string(sd[i:i+len(body)]) == string(body) {
				return
			}
		}
		t.Fatalf("accepted without an owner-only ACE for the SID: sd %x sid %x", sd, sid)
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
