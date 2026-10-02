// Package apputil is a sibling package of testdata/inspectapp — the
// request-parameter analysis must be able to descend into helper calls
// that cross the package boundary.
package apputil

import (
	"net/http"
	"net/url"
)

// Param reads one query parameter — the classic "request param read
// hidden inside a helper" shape. The parameter is renamed (req, not r).
func Param(req *http.Request, name string) string {
	return req.URL.Query().Get(name)
}

// FirstQuery takes the already-extracted url.Values instead of the
// request — req-ness must flow through the argument value, not the
// parameter's declared type.
func FirstQuery(q url.Values, name string) string {
	return q.Get(name)
}

// ReqOf returns its request argument unchanged — a passthrough helper
// whose return value stays request-derived.
func ReqOf(r *http.Request) *http.Request {
	return r
}
