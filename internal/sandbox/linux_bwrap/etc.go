package linux_bwrap

// The generated /etc files of design A06 §11.3, written by the launcher to
// S/sandbox/<sb_id>/etc/ and bound read-only by the argv (A06 §11.1).

import "fmt"

// EtcFileNames lists the generated files in argv order.
func EtcFileNames() []string {
	return []string{"passwd", "group", "hosts", "resolv.conf", "nsswitch.conf"}
}

// EtcFiles returns the content of each generated file. uid and gid are the
// host user's (bwrap maps them 1:1). Nothing comes from the host's /etc:
// resolv.conf names 127.0.0.1, where no resolver listens, so a lookup that
// bypasses the proxy fails fast instead of leaking the host resolver (A06
// §11.2).
func EtcFiles(uid, gid int) map[string]string {
	return map[string]string{
		"passwd": "root:x:0:0:root:/root:/usr/sbin/nologin\n" +
			fmt.Sprintf("warden:x:%d:%d:warden:/scratch/home:/usr/sbin/nologin\n", uid, gid),
		"group":         fmt.Sprintf("root:x:0:\nwarden:x:%d:\n", gid),
		"hosts":         "127.0.0.1 localhost\n::1 localhost\n",
		"resolv.conf":   "nameserver 127.0.0.1\noptions timeout:1 attempts:1\n",
		"nsswitch.conf": "passwd: files\ngroup: files\nhosts: files dns\n",
	}
}
