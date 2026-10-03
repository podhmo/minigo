// Package httppkg is the introspection subject for the inspect
// body-view experiment: net/http handlers whose request-parameter
// usage the script-side analyzer (testdata/inspectbody) recovers.
// It is only ever parsed and indexed — never executed.
package httppkg

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/podhmo/minigo/testdata/httphelp"
)

// CreateUser is the JSON body shape of CreateUserH.
type CreateUser struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// GetUser reads params directly and through helpers.
func GetUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query().Get("q")
	tok := r.Header.Get("X-Token")
	_, _, _ = id, q, tok

	values := r.URL.Query() // alias: values is request-rooted
	trace := values.Get("trace")
	_ = trace

	if r.URL.Query().Has("dbg") { // nested inside an IfStmt
		_, _ = r.Cookie("sess")
	}

	n, _ := strconv.Atoi(r.URL.Query().Get("n")) // conversion wrapper re-types
	_ = n

	r2 := pass(r) // the request flows out as a return value
	via := r2.URL.Query().Get("via")
	_ = via

	parseFilter(r)   // same-package helper
	httphelp.Page(r) // cross-package helper, two levels
}

// pass returns its argument — the request flows through a call's
// return value and keeps being trackable.
func pass(r *http.Request) *http.Request {
	return r
}

func parseFilter(r *http.Request) {
	f := r.URL.Query().Get("filter")
	c, err := r.Cookie("sess")
	_, _, _ = f, c, err
}

// CreateUserH reads through helpers only — the handler body itself
// touches no request member directly.
func CreateUserH(w http.ResponseWriter, r *http.Request) {
	decode(r)
	local(r)
}

func decode(r *http.Request) {
	var in CreateUser
	_ = json.NewDecoder(r.Body).Decode(&in)
}

func local(req *http.Request) { // renamed request parameter
	n := req.FormValue("note")
	_ = n
}

// GetItem is a method handler — Signature has a Recv.
func (s *Server) GetItem(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query().Get("v")
	_ = v
}

// Server is a dummy receiver type.
type Server struct{}

// Register wires routes; the analyzer binds these patterns to the
// handler decls.
func Register() {
	http.HandleFunc("GET /users/{id}", GetUser)
	http.HandleFunc("POST /users", CreateUserH)
	mux := http.NewServeMux()
	srv := &Server{}
	mux.HandleFunc("GET /items/{iid}", srv.GetItem)
	http.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("ping")
		_ = p
	})
}

func main() {}
