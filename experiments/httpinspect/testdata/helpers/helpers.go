package helpers

import web "net/http"

func init() { panic("helper init must never execute") }

func Query(r *web.Request, key string) string { return r.URL.Query().Get(key) }
func Identity(s string) string                { return s }

// Reader keeps request provenance on a concrete source receiver.
type Reader struct {
	Request *web.Request
	Key     string
}

func (x Reader) Read() string         { return Query(x.Request, x.Key) }
func (x *Reader) PointerRead() string { return Query(x.Request, x.Key) }
func (x *Reader) Rename(key string)   { x.Key = key }
func (x Reader) Bound() func() string { return func() string { return x.Read() } }
func Factory(r *web.Request, key string) func() string {
	return func() string { return Query(r, key) }
}
func Apply(read func() string) string { return read() }
func Nested(r *web.Request, key string) func() func() string {
	return func() func() string { return func() string { return Query(r, key) } }
}

func (x Reader) ReadAfter(_ string) string { return x.Read() }

func ReadWithArg(x Reader, _ string) string { return x.Read() }
