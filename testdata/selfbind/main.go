package main

import (
	"bytes"
	"context"
	"fmt"

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

func NestedModule() string {
	e := minigo.NewEngine(".")
	_, err := e.Run(context.Background(), "example.com/childmod/app", "Answer")
	if err != nil {
		return fmt.Sprint(err)
	}
	return "ok"
}

func NestedModeDeny() string {
	e := minigo.NewEngine(".")
	_, err := e.Run(context.Background(), "./testdata/selfbinddeny", "UseStrings")
	if err != nil {
		return "denied"
	}
	return "allowed"
}

func NestedResetHostPolicy() string {
	e := minigo.NewEngine(".")
	minigo.WithHostPolicy(nil)(e)
	_, err := e.Run(context.Background(), "./testdata/hostenv", "Read")
	if err != nil {
		return "denied"
	}
	return "allowed"
}

func NestedResetPackageModes() string {
	e := minigo.NewEngine(".")
	minigo.WithPackageModes(map[string]minigo.PackageMode{"strings": minigo.ModeAuto})(e)
	_, err := e.Run(context.Background(), "./testdata/selfbinddeny", "UseStrings")
	if err != nil {
		return "denied"
	}
	return "allowed"
}

func NestedMarker() string { return minigo.Marker() }
