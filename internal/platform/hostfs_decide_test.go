package platform

import (
	"errors"
	"testing"
)

// The HostFS decisions are pure and run on every OS, so a fail-open
// answer is caught on every CI runner, not only on the OS that asks.

// TestCaseSensitiveFS guards M2-18 review round 1 (CWE-178): a denylist of
// case-folding file systems called NFS, FUSE (ntfs-3g ignore_case, sshfs to
// a Mac, ciopfs) and virtiofs from a case-insensitive host case-sensitive,
// because they answer FS_IOC_GETFLAGS with ENOTTY. Only local file systems
// known to be case-sensitive are accepted; the casefold flag is checked on
// top of that; anything else is unknown, which the spec builder refuses.
func TestCaseSensitiveFS(t *testing.T) {
	const casefold = 0x40000000
	cases := []struct {
		name  string
		magic int64
		flags uint32
		ioctl ioctlResult
		want  bool
		err   bool // unknown: refused as worktree_case_unknown
	}{
		{"ext4", 0xef53, 0, ioctlOK, true, false},
		{"ext4 casefold dir", 0xef53, casefold, ioctlOK, false, false},
		{"ext4 other flags", 0xef53, 0x80000 | 0x10, ioctlOK, true, false},
		{"f2fs casefold dir", 0xf2f52010, casefold, ioctlOK, false, false},
		{"tmpfs casefold dir", 0x01021994, casefold, ioctlOK, false, false},
		{"tmpfs without GETFLAGS", 0x01021994, 0, ioctlUnsupported, true, false},
		{"xfs", 0x58465342, 0, ioctlOK, true, false},
		{"btrfs", 0x9123683e, 0, ioctlOK, true, false},
		{"overlay without GETFLAGS", 0x794c7630, 0, ioctlUnsupported, true, false},
		{"zfs", 0x2fc12fc1, 0, ioctlOK, true, false},
		{"bcachefs", 0xca451a4e, 0, ioctlOK, true, false},
		{"ext4 GETFLAGS failed", 0xef53, 0, ioctlFailed, false, true},
		{"nfs", 0x6969, 0, ioctlUnsupported, false, true},
		{"fuse (ntfs-3g, sshfs, ciopfs)", 0x65735546, 0, ioctlUnsupported, false, true},
		{"virtiofs (a FUSE mount)", 0x65735546, 0, ioctlOK, false, true},
		{"9p / WSL drvfs", 0x01021997, 0, ioctlUnsupported, false, true},
		{"vfat", 0x4d44, 0, ioctlOK, false, true},
		{"exfat", 0x2011bab0, 0, ioctlUnsupported, false, true},
		// NTFS mounts are refused here, which is also why HasShortAlias may
		// answer false on Linux: an NTFS volume whose DOS names Linux could
		// match (ntfs3, ntfs-3g) never passes this check.
		{"ntfs3", 0x7366746e, 0, ioctlOK, false, true},
		{"cifs", 0xff534d42, 0, ioctlUnsupported, false, true},
		{"smb2", 0xfe534d42, 0, ioctlUnsupported, false, true},
		{"unknown magic", 0x12345678, 0, ioctlOK, false, true},
	}
	for _, c := range cases {
		got, err := caseSensitiveFS(c.magic, c.flags, c.ioctl)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%s: caseSensitiveFS(%#x, %#x, %v) = %v, %v; want %v, err=%v", c.name, c.magic, c.flags, c.ioctl, got, err, c.want, c.err)
		}
		if err != nil && !errors.Is(err, ErrCaseUnknown) {
			t.Errorf("%s: error %v is not ErrCaseUnknown", c.name, err)
		}
	}
}

// TestShortNamesOn guards M2-18 review round 1 (CWE-41): value 3 of
// NtfsDisable8dot3NameCreation was read from the volume flag, but it
// means "every volume except the system volume", so C: kept creating
// short names; values above 3 were trusted too. The reviewer showed the
// creation check matters even after Build: in a case-sensitive directory
// on C:, `mkdir cfg; mv cfg configuration` gave the directory a new short
// name and `cat CONFIG~1/.env` printed the masked file.
func TestShortNamesOn(t *testing.T) {
	const off = shortNameCreationDisabled
	cases := []struct {
		name    string
		fs      string
		reg     uint64
		present bool
		vol     uint32
		system  bool
		want    bool
	}{
		{"0 enables everywhere, volume flag off", "NTFS", 0, true, off, false, true},
		{"0 on the system volume", "NTFS", 0, true, 0, true, true},
		{"1 disables everywhere", "NTFS", 1, true, 0, false, false},
		{"1 on the system volume", "NTFS", 1, true, 0, true, false},
		{"2 volume flag set", "NTFS", 2, true, off, false, false},
		{"2 volume flag set, system volume", "NTFS", 2, true, off, true, false},
		{"2 volume flag clear", "NTFS", 2, true, 0, false, true},
		{"3 system volume, flag set", "NTFS", 3, true, off, true, true},
		{"3 system volume, flag clear", "NTFS", 3, true, 0, true, true},
		{"3 other volume, flag clear", "NTFS", 3, true, 0, false, false},
		{"3 other volume, flag set", "NTFS", 3, true, off, false, false},
		{"4 unknown value", "NTFS", 4, true, off, false, true},
		{"huge unknown value", "NTFS", 1 << 40, true, off, false, true},
		{"value missing", "NTFS", 0, false, off, false, true},
		{"value missing, 1 ignored", "NTFS", 1, false, off, false, true},
		{"ReFS never", "ReFS", 0, true, 0, true, false},
		{"FAT32", "FAT32", 1, true, off, false, true},
		{"FAT", "FAT", 1, true, off, false, true},
		{"exFAT", "exFAT", 1, true, off, false, true},
		{"network or unknown", "CSC-CACHE", 1, true, off, false, true},
		{"empty fs name", "", 1, true, off, false, true},
		{"ntfs in another case is not NTFS", "ntfs", 1, true, off, false, true},
	}
	for _, c := range cases {
		if got := shortNamesOn(c.fs, c.reg, c.present, c.vol, c.system); got != c.want {
			t.Errorf("%s: shortNamesOn(%q, %d, %v, %#x, %v) = %v, want %v", c.name, c.fs, c.reg, c.present, c.vol, c.system, got, c.want)
		}
	}
}

// HomeHash is the D-042 hash, but for every home including the default
// one: the L2 label warden.home must tell two homes sharing an engine
// apart (M2-18 review round 1).
func TestHomeHash(t *testing.T) {
	a, b := HomeHash("/srv/a/.warden"), HomeHash("/srv/b/.warden")
	if len(a) != 8 || a == b {
		t.Fatalf("HomeHash = %q, %q; want two different 8-hex values", a, b)
	}
	if got := homeSuffix("/srv/a/.warden", "/srv/other"); got != "-"+a {
		t.Fatalf("homeSuffix = %q, want -%s (one hash, D-042)", got, a)
	}
	def, err := DefaultHome()
	if err != nil {
		t.Skip(err)
	}
	if h := HomeHash(def); len(h) != 8 {
		t.Fatalf("HomeHash(default) = %q, want 8 hex digits even for the default home", h)
	}
}
