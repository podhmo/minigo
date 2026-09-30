// Package bogus exists only to prove laziness: it is referenced solely
// inside a dead branch of the DSL file's quoted argument. If the special
// call fired, the handler would scan this package and register a second
// rule; if the interpreter materialized the import, the spy resolver
// would record it. Neither happens, so the package is never read.
package bogus

import "time"

func Nope(t time.Time) string {
	return t.String()
}
