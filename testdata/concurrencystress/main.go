// Package main contains shared-engine-safe stress fixtures. Every invocation
// owns its mutable state; no fixture prints or modifies a package global.
package main

import (
	"bytes"
	"reflect"
	"sort"
	"sync"
	"text/template"
)

const width = 3

type vector [width]int

func (v vector) Total() int { return v[0] + v[1] + v[2] }

type totaler interface{ Total() int }

// ArrayTypes combines nonliteral/nested lengths, reflection and interface
// dispatch. Each new engine makes the first concurrent calls start cold.
func ArrayTypes() int {
	v := vector{1, 2, 3}
	var value totaler = v
	var nested [width][width + 1]int
	ellipsis := [...]int{1, 2, 3}
	return value.Total() + reflect.TypeOf(v).Len() + len(nested[0]) + len(ellipsis)
}

// SyncReentry invokes script closures through two synchronous host APIs.
func SyncReentry() int {
	var once sync.Once
	n := 0
	once.Do(func() { n++ })
	once.Do(func() { n++ })
	values := []int{3, 1, 2}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return n + values[0]
}

// FuncMap keeps both the template and its output local to one invocation.
func FuncMap() string {
	t := template.Must(template.New("stress").Funcs(template.FuncMap{
		"sum": func(a, b int) int { return a + b },
	}).Parse("{{sum 2 4}}"))
	var out bytes.Buffer
	if err := t.Execute(&out, nil); err != nil {
		panic(err)
	}
	return out.String()
}
