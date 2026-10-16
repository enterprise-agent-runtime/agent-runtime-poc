package api

import "crypto/tls"

func tlsListen() {
	_, _ = tls.Listen("tcp", "127.0.0.1:0", nil)
	_ = tls.NewListener(nil, nil)
}
