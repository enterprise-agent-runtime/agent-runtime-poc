package api

import (
	"net"
	"net/http"
)

func f() {
	_, _ = net.Listen("tcp", "127.0.0.1:0")
	_, _ = net.Listen("unix", "/tmp/x.sock")
	_ = http.ListenAndServe(":0", nil)
}
