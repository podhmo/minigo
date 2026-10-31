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
