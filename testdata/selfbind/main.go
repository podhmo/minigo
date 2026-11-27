package main

import (
	"bytes"
	"context"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/resolve"
)

func NestedSession() string {
	e := minigo.NewEngine(".",
		minigo.WithBuildConfig(resolve.BuildConfig{Tags: []string{"story"}}),
		minigo.WithPackageModes(map[string]minigo.PackageMode{"example.com/unused": minigo.ModeSource}),
	)
	s := e.NewSession()
	var out bytes.Buffer
	minigo.WithOutput(&out)(s)
	return "ok"
}

func NestedHostPolicy() string {
	e := minigo.NewEngine(".")
	_, err := e.Run(context.Background(), "./testdata/hostenv", "Read")
	if err != nil {
		return "denied"
	}
	return "allowed"
}
