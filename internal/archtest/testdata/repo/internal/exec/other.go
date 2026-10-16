package exec

import "net"

func other() { _, _ = net.Listen("tcp", "127.0.0.1:3128") }
