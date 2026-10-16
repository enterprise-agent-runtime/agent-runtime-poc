package api

import "net/http"

func serve() {
	s := &http.Server{Addr: "127.0.0.1:0"}
	_ = s.ListenAndServe()
}
