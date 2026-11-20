package main

import (
	"encoding/json"

	"example.com/hostwb"
)

// Change hands a slice of script maps to a bound host func that replaces
// rows[0]: the replacement must reach the caller, the untouched rows[1]
// must stay the caller's script map, and both must stay editable.
func Change() string {
	rows := []map[string]int{{"old": 1}, {"keep": 3}}
	hostwb.Change(rows)
	b, err := json.Marshal(rows)
	if err != nil {
		return "err: " + err.Error()
	}
	out := string(b)
	rows[0]["x"] = 5
	rows[1]["more"] = 4
	b, _ = json.Marshal(rows)
	return out + "\n" + string(b)
}

// Touch hands a script map to a bound host func that adds, rewrites and
// deletes entries in place; the writes must reach the caller's map. It
// also mutates a nested map reached through a slice arg.
func Touch() string {
	m := map[string]int{"keep": 1, "gone": 2, "old": 3}
	rows := []map[string]int{{"a": 1}, {"b": 2}}
	hostwb.Touch(m, rows)
	b, err := json.Marshal(m)
	if err != nil {
		return "err: " + err.Error()
	}
	b2, err := json.Marshal(rows)
	if err != nil {
		return "err: " + err.Error()
	}
	return string(b) + " " + string(b2)
}
