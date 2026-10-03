// Package httphelp is the cross-package helper half of the inspect
// body-view experiment: request-parameter reads hiding behind an
// import boundary and a second-level call.
package httphelp

import "net/http"

// Page reads a pagination param, then hands the request deeper.
func Page(r *http.Request) {
	p := r.URL.Query().Get("page")
	deep(r)
	_ = p
}

func deep(rr *http.Request) { // renamed again — param mapping follows
	s := rr.URL.Query().Get("size")
	_ = s
}
