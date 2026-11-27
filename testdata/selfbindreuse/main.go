package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/podhmo/minigo"
)

type scriptWriter struct{}

var nested *minigo.Engine
var written int

func (scriptWriter) Write(p []byte) (int, error) {
	written += len(p)
	return len(p), nil
}

func Setup() string {
	nested = minigo.NewEngine(".")
	minigo.WithOutput(scriptWriter{})(nested)
	return "ok"
}

func Use() string {
	if _, err := nested.Run(context.Background(), "./testdata/intrins", "StdoutWriteString"); err != nil {
		return fmt.Sprint(err)
	}
	return fmt.Sprint(written)
}

func SetupPolicy() string {
	nested = minigo.NewEngine(".")
	minigo.WithHostPolicy(func(path, symbol string) bool { return symbol != "Getenv" })(nested)
	return "ok"
}

func UsePolicy() string {
	_, err := nested.Run(context.Background(), "./testdata/hostenv", "Read")
	if err == nil {
		return "allowed"
	}
	if strings.Contains(fmt.Sprint(err), "Getenv") {
		return "denied"
	}
	return fmt.Sprint(err)
}
