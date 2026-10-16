package api

import . "crypto/tls"

func dotTLS() { _, _ = Listen("tcp", "127.0.0.1:0", nil) }
