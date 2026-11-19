package vm

import "strings"

// ifaceReqHas answers whether an interface requirement set promises a
// member selectable as `name`. Unexported requirements carry their
// declaring package's key ("a/b.m" — see reqKey in the engine), so the
// plain name needs a suffix match; method names never contain '.'.
func ifaceReqHas(reqs map[string]bool, name string) bool {
	if reqs[name] {
		return true
	}
	suffix := "." + name
	for k := range reqs {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}
