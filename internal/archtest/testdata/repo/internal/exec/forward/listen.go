package forward

import "net"

func listen() { _, _ = net.Listen("tcp", "127.0.0.1:3128") }
