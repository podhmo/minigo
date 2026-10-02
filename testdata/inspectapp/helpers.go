package inspectapp

import "net/http"

// localQuery is a same-package helper in a different file: the query
// read lives two levels down when reached through withDefault.
func localQuery(req *http.Request, name string) string {
	return req.URL.Query().Get(name)
}

// withDefault adds a second level of indirection.
func withDefault(r2 *http.Request, name, fallback string) string {
	v := localQuery(r2, name)
	if v == "" {
		return fallback
	}
	return v
}
