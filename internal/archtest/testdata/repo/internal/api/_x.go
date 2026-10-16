package skipped

import "net"

func l() { _, _ = net.Listen("tcp", "127.0.0.1:0") }
