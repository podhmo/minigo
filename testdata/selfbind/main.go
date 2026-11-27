package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

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
		return fmt.Sprint(err)
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
		return fmt.Sprint(err)
	}
	return "allowed"
}

func NestedRecorder() string {
	e := minigo.NewEngine(".")
	pkg, err := e.Package(context.Background(), "./testdata/selfbindrec")
	if err != nil {
		return fmt.Sprint(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := e.Call(context.Background(), pkg, "Write", rec, req); err != nil {
		return fmt.Sprint(err)
	}
	res := rec.Result()
	return fmt.Sprintf("%d|%s|%s", res.StatusCode, res.Header.Get("X-Test"), rec.Body.String())
}

func NestedSliceIsolation() string {
	e := minigo.NewEngine(".")
	pkg, err := e.Package(context.Background(), "./testdata/selfbindrec")
	if err != nil {
		return fmt.Sprint(err)
	}
	xs := []string{"orig"}
	if _, err := e.Call(context.Background(), pkg, "Mutate", xs); err != nil {
		return fmt.Sprint(err)
	}
	return xs[0]
}

type scriptWriter struct{ n *int }

func (w scriptWriter) Write(p []byte) (int, error) {
	*w.n += len(p)
	return len(p), nil
}

func NestedOutputWriter() string {
	n := 0
	e := minigo.NewEngine(".")
	minigo.WithOutput(scriptWriter{n: &n})(e)
	if _, err := e.Run(context.Background(), "./testdata/intrins", "StdoutWriteString"); err != nil {
		return fmt.Sprint(err)
	}
	return fmt.Sprint(n)
}

type writeStringInner int

func (writeStringInner) WriteString(s string) (int, error) {
	return 0, errors.New("write string failed")
}

type embeddedWriteStringWriter struct{ *writeStringInner }

func (embeddedWriteStringWriter) Write(p []byte) (int, error) { return len(p), nil }

func NestedNilEmbeddedWriteString() string {
	e := minigo.NewEngine(".")
	minigo.WithOutput(embeddedWriteStringWriter{})(e)
	r, err := e.Run(context.Background(), "./testdata/intrins", "StdoutWriteStringErr")
	if err != nil {
		if strings.Contains(fmt.Sprint(err), "write string failed") || strings.Contains(fmt.Sprint(err), "nil pointer") {
			return "error"
		}
		return fmt.Sprint(err)
	}
	if strings.Contains(fmt.Sprint(r), "write string failed") || strings.Contains(fmt.Sprint(r), "nil pointer") {
		return "error"
	}
	return "no error"
}

func NestedHostPolicyScriptFunc() string {
	e := minigo.NewEngine(".")
	minigo.WithHostPolicy(func(path, symbol string) bool { return symbol != "Getenv" })(e)
	if _, err := e.Run(context.Background(), "./testdata/hostenv", "Read"); err != nil {
		return "denied"
	}
	return "allowed"
}

func NestedMarker() string { return minigo.Marker() }
