// Package inspectapp is the analysis subject: a net/http-style webapp
// whose handlers read request parameters through a representative mix
// of direct reads, helper functions (same package and cross-package),
// aliases, re-typing conversions, method handlers and func literals.
// The analyzer (testdata/inspectbody) must recover every parameter.
package inspectapp

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/podhmo/minigo/testdata/inspectapp/apputil"
)

// Payload is the JSON body schema of UpdateItem.
type Payload struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ListItems reads: q (query), n (query→int), tag (query via index),
// X-Token (header), sort+theme (query via helpers).
func ListItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	_ = q
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	_ = n
	tags := r.URL.Query()["tag"]
	_ = tags
	tok := r.Header.Get("X-Token")
	_ = tok
	sort := localQuery(r, "sort")
	_ = sort
	theme := withDefault(r, "theme", "light")
	_ = theme
	limit := apputil.Param(r, "limit")
	_ = limit
	first := apputil.FirstQuery(r.URL.Query(), "first")
	_ = first
	alias := r
	flag := alias.URL.Query().Get("flag")
	_ = flag
	other := apputil.ReqOf(r).URL.Query().Get("other")
	_ = other
	enc := json.NewEncoder(w)
	_ = enc.Encode(map[string]string{"ok": "true"})
}

// GetItem reads: id (path), note (form), sess (cookie).
func GetItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = id
	note := r.FormValue("note")
	_ = note
	sess, _ := r.Cookie("sess")
	_ = sess
	w.WriteHeader(200)
}

// UpdateItem reads a JSON body into a typed struct (body schema).
func UpdateItem(w http.ResponseWriter, r *http.Request) {
	var p Payload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		w.WriteHeader(400)
		return
	}
	_ = p
	w.WriteHeader(204)
}

// Server is a receiver for method handlers.
type Server struct {
	Prefix string
}

// DeleteItem is a method handler: id (path), reason (query via helper).
func (s *Server) DeleteItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = id
	reason := apputil.Param(r, "reason")
	_ = reason
	w.WriteHeader(204)
}

// Routes registers handlers — route patterns carry the path params.
func Routes(srv *Server) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items", ListItems)
	mux.HandleFunc("GET /items/{id}", GetItem)
	mux.HandleFunc("PUT /items/{id}", UpdateItem)
	mux.HandleFunc("DELETE /items/{id}", srv.DeleteItem)
	mux.HandleFunc("POST /echo/{word}", func(w http.ResponseWriter, r *http.Request) {
		f := r.FormValue("f")
		_ = f
		word := r.PathValue("word")
		_ = word
		w.Write([]byte("ok"))
	})
}
