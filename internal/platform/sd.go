package platform

import (
	"bytes"
	"encoding/binary"
)

// Self-relative security descriptor layout (winnt.h SECURITY_DESCRIPTOR_RELATIVE,
// ACL, ACE_HEADER, ACCESS_ALLOWED_ACE, SID). Kept untagged and pure so the
// owner-only decision is tested on every OS (CLAUDE.md §4 IPC, DECISIONS D-011).
const (
	sdHeaderSize  = 20 // Revision, Sbz1, Control, OffsetOwner, OffsetGroup, OffsetSacl, OffsetDacl
	aclHeaderSize = 8  // AclRevision, Sbz1, AclSize, AceCount, Sbz2
	aceHeaderSize = 4  // AceType, AceFlags, AceSize
	sidHeaderSize = 8  // Revision, SubAuthorityCount, IdentifierAuthority[6]

	seDACLPresent   = 0x0004
	seDACLProtected = 0x1000
	seSelfRelative  = 0x8000

	accessAllowedACEType = 0
	fileAllAccess        = 0x001F01FF // FILE_ALL_ACCESS, rendered "FA"
)

// ownerOnlyDACL reports whether sd, a self-relative security descriptor,
// carries a protected DACL with exactly one ACE: ACCESS_ALLOWED, no flags
// (in particular not inherited), mask FILE_ALL_ACCESS, for exactly sid.
// The DACL is compared structurally because its SDDL text is not canonical:
// Windows abbreviates well-known SIDs ("LA", "BA") when rendering it.
// Every offset and length is bounds-checked; malformed input yields false.
func ownerOnlyDACL(sd, sid []byte) bool {
	if len(sd) < sdHeaderSize || !validSID(sid) {
		return false
	}
	control := binary.LittleEndian.Uint16(sd[2:])
	if control&seSelfRelative == 0 || control&seDACLPresent == 0 || control&seDACLProtected == 0 {
		return false
	}
	off := uint64(binary.LittleEndian.Uint32(sd[16:]))
	// Offset 0 is a NULL DACL (everyone, everything); any other offset below
	// the header would read the ACL out of the header's own fields.
	if off < sdHeaderSize || off+aclHeaderSize > uint64(len(sd)) {
		return false
	}
	acl := sd[off:]
	aclSize := int(binary.LittleEndian.Uint16(acl[2:]))
	if aclSize < aclHeaderSize || aclSize > len(acl) || binary.LittleEndian.Uint16(acl[4:]) != 1 {
		return false
	}
	ace := acl[aclHeaderSize:aclSize]
	if len(ace) < aceHeaderSize+4 {
		return false
	}
	aceSize := int(binary.LittleEndian.Uint16(ace[2:]))
	if aceSize < aceHeaderSize+4 || aceSize > len(ace) {
		return false
	}
	ace = ace[:aceSize]
	if ace[0] != accessAllowedACEType || ace[1] != 0 || binary.LittleEndian.Uint32(ace[4:]) != fileAllAccess {
		return false
	}
	got := ace[aceHeaderSize+4:]
	if !validSID(got) {
		return false
	}
	return bytes.Equal(got[:sidLen(got)], sid)
}

// validSID reports whether b starts with a well-formed SID of exactly the
// length its SubAuthorityCount declares, or longer (trailing ACE padding).
func validSID(b []byte) bool {
	return len(b) >= sidHeaderSize && b[0] == 1 && b[1] >= 1 && len(b) >= sidLen(b)
}

func sidLen(b []byte) int { return sidHeaderSize + 4*int(b[1]) }
