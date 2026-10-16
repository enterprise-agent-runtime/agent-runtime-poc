package platform

import (
	"bytes"
	"encoding/binary"
	"fmt"
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
	return ownerIs(sd, sid) && singleACEDACL(sd, sid, aceFlags)
}

// ownerIs reports whether the descriptor's owner is exactly sid.
func ownerIs(sd, sid []byte) bool {
	return len(sd) >= sdHeaderSize && sidAt(sd, binary.LittleEndian.Uint32(sd[4:]), sid)
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

// builtinAdministratorsSID is S-1-5-32-544, the default owner of what an
// elevated administrator's process creates.
func builtinAdministratorsSID() []byte {
	return []byte{1, 2, 0, 0, 0, 0, 0, 5, 32, 0, 0, 0, 0x20, 0x02, 0, 0}
}

// foreignOwnerError is protectDir's refusal of a directory another account
// owns; detail renders the descriptor. The refusal stands whoever the owner
// is: BUILTIN\Administrators is not accepted either, since every elevated
// process of every administrator on the machine could then change it. But
// when it is the owner, the directory was almost certainly created earlier
// by a Warden run under an elevated token (an administrator's prompt), and
// "use a directory of your own" sends the user looking for another account,
// so the message names that cause and the fix (WH-4).
func foreignOwnerError(dir string, sd []byte, detail string) error {
	if ownerIs(sd, builtinAdministratorsSID()) {
		return fmt.Errorf(`%s is owned by BUILTIN\Administrators (%s), not by you: it was probably created by Warden running elevated (as administrator). `+
			`Remove it, or take ownership of it from an elevated prompt (takeown /f "%s" /r), then run Warden without elevation`, dir, detail, dir)
	}
	return fmt.Errorf("%s is owned by another account (%s), not by you; use a directory of your own", dir, detail)
}

// ACE types and flags read by noForeignWriter (winnt.h).
const (
	accessDeniedACEType         = 1
	accessAllowedObjectACEType  = 5
	accessDeniedObjectACEType   = 6
	accessAllowedCallbackType   = 9
	accessDeniedCallbackType    = 10
	accessAllowedCallbackObject = 11
	accessDeniedCallbackObject  = 12
	inheritOnlyACE              = 0x08

	// fileWriteRights are the rights that let a holder change a file's
	// content or its descriptor: FILE_WRITE_DATA, FILE_APPEND_DATA, DELETE,
	// WRITE_DAC, WRITE_OWNER, GENERIC_WRITE, GENERIC_ALL.
	fileWriteRights = 0x00000002 | 0x00000004 | 0x00010000 | 0x00040000 | 0x00080000 | 0x40000000 | 0x10000000
)

// trustedWriterSIDs may hold write rights on a file the user owns without
// making it untrusted: the user, OWNER RIGHTS (S-1-3-4, the owner itself),
// SYSTEM (S-1-5-18) and BUILTIN\Administrators (S-1-5-32-544). The last two
// are on every inherited DACL in a profile and can take ownership of any
// file anyway, so refusing them would refuse every key file a build before
// WH-4 wrote and protect against nobody (the Unix analogue is root).
func trustedWriterSIDs(user []byte) [][]byte {
	return [][]byte{
		user,
		{1, 1, 0, 0, 0, 0, 0, 3, 4, 0, 0, 0},
		{1, 1, 0, 0, 0, 0, 0, 5, 18, 0, 0, 0},
		builtinAdministratorsSID(),
	}
}

// noForeignWriter reports whether the DACL of sd grants no write-type right
// (fileWriteRights) to any SID other than the trusted writers: the Windows
// integrity decision for a file whose content must be the user's, though
// anyone may read it (checkpoint public keys, WH-4 round 1). A NULL or
// missing DACL grants everything and is refused. Allow ACEs of the object
// and callback kinds are refused rather than parsed (fail closed); deny
// ACEs are ignored, which can only make the answer stricter. Inherit-only
// ACEs do not apply to the file itself. The owner is not looked at; the
// caller checks it with ownerIs.
func noForeignWriter(sd, user []byte) bool {
	if len(sd) < sdHeaderSize || !validSID(user) {
		return false
	}
	control := binary.LittleEndian.Uint16(sd[2:])
	if control&seSelfRelative == 0 || control&seDACLPresent == 0 {
		return false
	}
	off := uint64(binary.LittleEndian.Uint32(sd[16:]))
	if off < sdHeaderSize || off+aclHeaderSize > uint64(len(sd)) {
		return false
	}
	acl := sd[off:]
	aclSize := int(binary.LittleEndian.Uint16(acl[2:]))
	if aclSize < aclHeaderSize || aclSize > len(acl) {
		return false
	}
	count := int(binary.LittleEndian.Uint16(acl[4:]))
	aces := acl[aclHeaderSize:aclSize]
	for i := 0; i < count; i++ {
		if len(aces) < aceHeaderSize {
			return false
		}
		size := int(binary.LittleEndian.Uint16(aces[2:]))
		if size < aceHeaderSize || size > len(aces) {
			return false
		}
		ace := aces[:size]
		aces = aces[size:]
		if ace[1]&inheritOnlyACE != 0 {
			continue
		}
		switch ace[0] {
		case accessDeniedACEType, accessDeniedObjectACEType, accessDeniedCallbackType, accessDeniedCallbackObject:
			continue
		case accessAllowedACEType:
		default: // allowed object/callback ACEs and anything unknown
			return false
		}
		if len(ace) < aceHeaderSize+4 {
			return false
		}
		if binary.LittleEndian.Uint32(ace[4:])&fileWriteRights == 0 {
			continue
		}
		sid := ace[aceHeaderSize+4:]
		if !validSID(sid) || !trustedWriter(sid[:sidLen(sid)], user) {
			return false
		}
	}
	return true
}

func trustedWriter(sid, user []byte) bool {
	for _, t := range trustedWriterSIDs(user) {
		if bytes.Equal(sid, t) {
			return true
		}
	}
	return false
}

// ownerWritableMode is the Unix integrity decision for a file anyone may
// read: owned by uid, and neither group nor others may write it (WH-4
// round 1). Compare ownerOnlyMode, which is about secrecy.
func ownerWritableMode(perm os.FileMode, owner, uid int) bool {
	return perm&0o022 == 0 && owner == uid
}
