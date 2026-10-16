package api

import "net"

func fileListener() { _, _ = net.FileListener(nil) }
