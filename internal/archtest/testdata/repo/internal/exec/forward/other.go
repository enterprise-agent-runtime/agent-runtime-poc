package forward

import "net"

func other() { _, _ = net.ListenTCP("tcp", nil) }
