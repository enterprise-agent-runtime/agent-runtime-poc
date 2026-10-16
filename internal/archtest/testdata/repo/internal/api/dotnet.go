package api

import . "net"

func dotListen() { _, _ = Listen("tcp", "127.0.0.1:0") }
