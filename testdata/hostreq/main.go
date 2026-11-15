package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
)

// Dispatch routes a host *http.Request through an interpreted
// ServeMux. ServeHTTP stamps the routing internals (Pattern, pat,
// matches) on the host request itself, so the dispatched handler reads
// them back through PathValue and Pattern — no host-side rebuild
// (issue 1082). SetPathValue mutates the stamped match, which compiled
// Go exposes through the same fields.
func Dispatch(w http.ResponseWriter, r *http.Request) string {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /issues/{id}/comments", func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", "3")
		fmt.Fprintf(w, "id=%s pattern=%s", r.PathValue("id"), r.Pattern)
	})
	mux.ServeHTTP(w, r)
	return "dispatched"
}

type ctxKey struct{}

// DispatchMW wraps dispatch in an interpreted middleware: WithContext
// on the host request returns a real clone, the mux stamps the clone,
// and the handler reads both the path value and the context value.
func DispatchMW(w http.ResponseWriter, r *http.Request) string {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v/{k}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "k=%s ctxv=%v", r.PathValue("k"), r.Context().Value(ctxKey{}))
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, "from-mw")))
	})
	next.ServeHTTP(w, r)
	return "dispatched"
}

// PostForm is the storybook parseForm path applied to the host request
// itself: MaxBytesReader installs a script-side reader into the real
// r.Body, ParseForm reads through it, and PostFormValue answers.
// A multipart request returns "" here — compiled Go leaves
// PostFormValue empty once ParseForm has run, so this locks the
// standard semantics reported in issue 1084 rather than a divergence.
func PostForm(w http.ResponseWriter, r *http.Request) string {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /issues/{id}/comments", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, "id=%s body=%q", r.PathValue("id"), r.PostFormValue("body"))
	})
	mux.ServeHTTP(w, r)
	return "dispatched"
}

// ExplicitMultipart calls ParseMultipartForm on the host request —
// multipart fields parse through the real body when asked for
// directly (issue 1084).
func ExplicitMultipart(w http.ResponseWriter, r *http.Request) string {
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		fmt.Fprintf(w, "err=%v", err)
		return "failed"
	}
	fmt.Fprintf(w, "body=%q", r.PostFormValue("body"))
	return "dispatched"
}

// WritePrivateField tries a package-foreign unexported write — Go
// rejects it at compile time, and the interpreter must reject it too
// instead of writing through to the host request.
func WritePrivateField(r *http.Request) string {
	r.pat = nil
	return "unreachable"
}

// ReadPrivateField tries a package-foreign unexported read.
func ReadPrivateField(r *http.Request) string {
	return fmt.Sprint(r.pat)
}

// ReaderBody is issue 1083: httptest.NewRequest's body type switch
// needs the bound bytes package to expose *bytes.Reader.
func ReaderBody() string {
	req := httptest.NewRequest("POST", "/issues/2/comments", bytes.NewReader([]byte("body=hi")))
	b, err := io.ReadAll(req.Body)
	if err != nil {
		return "read: " + err.Error()
	}
	return fmt.Sprint("cl=", req.ContentLength, " body=", string(b))
}

func main() {}
