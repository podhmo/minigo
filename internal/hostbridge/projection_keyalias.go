package hostbridge

import "reflect"

// canonicalKeyAliases groups an interface-keyed host map's keys by their
// script-canonical spelling. Concrete-keyed maps need no alias cleanup.
func canonicalKeyAliases(rv reflect.Value) map[string][]reflect.Value {
	if rv.Type().Key().Kind() != reflect.Interface {
		return nil
	}
	out := map[string][]reflect.Value{}
	for _, mk := range rv.MapKeys() {
		key := projKey(GoSlotValueOf(mk))
		out[key] = append(out[key], mk)
	}
	return out
}

// dropCanonicalKeyAliases removes interface-map keys that spell the same
// script key as hk under another host type. A host write can add int(1)
// beside the projection's canonical int64(1); once pull folds it into the
// script key, keeping both host spellings would duplicate one script entry.
func dropCanonicalKeyAliases(rv, hk reflect.Value, keys []reflect.Value) {
	for _, mk := range keys {
		if sameHostKeyValue(mk, hk) {
			continue
		}
		rv.SetMapIndex(mk, reflect.Value{})
	}
}

func sameHostKeyValue(a, b reflect.Value) bool {
	for a.Kind() == reflect.Interface && !a.IsNil() {
		a = a.Elem()
	}
	for b.Kind() == reflect.Interface && !b.IsNil() {
		b = b.Elem()
	}
	if !a.IsValid() || !b.IsValid() {
		return !a.IsValid() && !b.IsValid()
	}
	return a.Type() == b.Type() && reflect.DeepEqual(a.Interface(), b.Interface())
}
