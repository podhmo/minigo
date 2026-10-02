package helpers

import web "net/http"

func init() { panic("helper init must never execute") }

func Query(r *web.Request, key string) string { return r.URL.Query().Get(key) }
func Identity(s string) string                { return s }
