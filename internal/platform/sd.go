package platform

import (
	"bytes"
	"encoding/binary"
	"os"
)

// Self-relative security descriptor layout (winnt.h SECURITY_DESCRIPTOR_RELATIVE,
// ACL, ACE_HEADER, ACCESS_ALLOWED_ACE, SID). Kept untagged and pure so the
// owner-only decision is tested on every OS (CLAUDE.md §4 IPC, DECISIONS D-011,
// D-027).
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

	objectInheritACE    = 0x01 // OI: inherited by files
	containerInheritACE = 0x02 // CI: inherited by subdirectories
)

// ownerOnlyDACL reports whether sd, a self-relative security descriptor,
// is the owner-only descriptor of a file: owned by exactly sid, and carrying
// a protected DACL with exactly one ACE: ACCESS_ALLOWED, no flags (in
// particular not inherited), mask FILE_ALL_ACCESS, for exactly sid.
// The owner matters because it holds READ_CONTROL and WRITE_DAC whatever the
// DACL says (WH-1+2, security review of FX-3, CWE-283).
// The DACL is compared structurally because its SDDL text is not canonical:
// Windows abbreviates well-known SIDs ("LA", "BA") when rendering it.
// Every offset and length is bounds-checked; malformed input yields false.
func ownerOnlyDACL(sd, sid []byte) bool { return ownerOnlySD(sd, sid, 0) }

// ownerOnlyDirDACL is ownerOnlyDACL for a directory whose one ACE is
// inherited by its files and subdirectories (OI|CI), as Ensure() writes it
// on the home and run directories (WH-1+2).
func ownerOnlyDirDACL(sd, sid []byte) bool {
	return ownerOnlySD(sd, sid, objectInheritACE|containerInheritACE)
}

func ownerOnlySD(sd, sid []byte, aceFlags byte) bool {
	return len(sd) >= sdHeaderSize && sidAt(sd, binary.LittleEndian.Uint32(sd[4:]), sid) && singleACEDACL(sd, sid, aceFlags)
}

// singleACEDACL is the DACL half of ownerOnlySD: present, protected, one
// ACCESS_ALLOWED ACE with exactly aceFlags and FILE_ALL_ACCESS for sid. The
// owner is not looked at.
func singleACEDACL(sd, sid []byte, aceFlags byte) bool {
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
	if ace[0] != accessAllowedACEType || ace[1] != aceFlags || binary.LittleEndian.Uint32(ace[4:]) != fileAllAccess {
		return false
	}
	got := ace[aceHeaderSize+4:]
	if !validSID(got) {
		return false
	}
	return bytes.Equal(got[:sidLen(got)], sid)
}

// sidAt reports whether the descriptor holds exactly sid at offset off.
// Offset 0 means "no owner"; an offset inside the header would read the SID
// out of the header's own fields.
func sidAt(sd []byte, off uint32, sid []byte) bool {
	if uint64(off) < sdHeaderSize || uint64(off) >= uint64(len(sd)) {
		return false
	}
	b := sd[off:]
	return validSID(b) && bytes.Equal(b[:sidLen(b)], sid)
}

// validSID reports whether b starts with a well-formed SID of exactly the
// length its SubAuthorityCount declares, or longer (trailing ACE padding).
func validSID(b []byte) bool {
	return len(b) >= sidHeaderSize && b[0] == 1 && b[1] >= 1 && len(b) >= sidLen(b)
}

func sidLen(b []byte) int { return sidHeaderSize + 4*int(b[1]) }

// ownerOnlyMode is the Unix owner-only decision: no group or other bits,
// and owned by the caller's uid. A 0600 file of another uid is not
// owner-only: its owner can read it and chmod it (WH-1+2, CWE-283).
func ownerOnlyMode(perm os.FileMode, owner, uid int) bool {
	return perm&0o077 == 0 && owner == uid
}
