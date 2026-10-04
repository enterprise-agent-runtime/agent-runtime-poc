package main

import "net"

func main() { _, _ = net.Listen("tcp", ":3128") }
