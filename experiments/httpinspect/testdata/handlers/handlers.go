package handlers

import (
	web "net/http"
	"strconv"
	"strings"

	util "github.com/podhmo/minigo/experiments/httpinspect/testdata/helpers"
)

func init() { panic("handler init must never execute") }

func Direct(_ web.ResponseWriter, r *web.Request) {
	_ = r.URL.Query().Get("search")
	_ = r.Header.Get("X-Token")
	_ = r.PathValue("id")
}
func query(r *web.Request, key string) string { return util.Query(r, key) }
func Helper(_ web.ResponseWriter, r *web.Request) {
	raw := query(r, "limit")
	raw = util.Identity(raw)
	_, _ = strconv.Atoi(raw)
}
func Branch(_ web.ResponseWriter, r *web.Request) {
	if r == nil {
		_ = query(r, "left")
	} else {
		_ = query(r, "right")
	}
	{
		query := r.Header
		_ = query.Get("X-Local")
	}
	_ = query(r, "after")
}
func recurse(r *web.Request) string                  { _ = query(r, "before-depth"); return recurse(r) }
func Recursive(_ web.ResponseWriter, r *web.Request) { _ = recurse(r) }
func Dynamic(_ web.ResponseWriter, r *web.Request)   { key := r.Header.Get("X-Key"); _ = query(r, key) }
func Loop(_ web.ResponseWriter, r *web.Request) {
	for i := 0; i < 3; i++ {
		_ = query(r, "loop")
	}
}
func Opaque(_ web.ResponseWriter, r *web.Request) { _ = strings.TrimSpace(query(r, "opaque")) }
func ReturnBranch(r *web.Request) string {
	if r == nil {
		return query(r, "early")
	}
	return query(r, "late")
}
func BranchResult(_ web.ResponseWriter, r *web.Request) { _, _ = strconv.Atoi(ReturnBranch(r)) }
func InspectOnly(r *web.Request)                        { var local *web.Request; _ = local; _ = r }

func ShadowImport(_ web.ResponseWriter, r *web.Request) {
	strconv := r.Header
	_ = strconv.Get("X-Shadow")
}
func IfShadow(_ web.ResponseWriter, r *web.Request) {
	key := "outer"
	if key := r.Header.Get("X-Inner"); key == "" {
		_ = query(r, "inside")
	}
	_ = query(r, key)
}
func Named(r *web.Request, key string) (result string) { result = query(r, key); return }
func NamedResult(_ web.ResponseWriter, r *web.Request) { _, _ = strconv.Atoi(Named(r, "named")) }
func UnknownMutation(_ web.ResponseWriter, r *web.Request) {
	key := "old"
	for i := 0; i < 3; i++ {
		key = "changed"
	}
	_ = query(r, key)
}
func Closure(r *web.Request) { f := func() string { return r.URL.Query().Get("closure") }; _ = f() }
