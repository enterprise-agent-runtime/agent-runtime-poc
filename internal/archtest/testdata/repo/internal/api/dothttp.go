package api

import . "net/http"

func dotServe() { _ = ListenAndServe(":0", nil) }
