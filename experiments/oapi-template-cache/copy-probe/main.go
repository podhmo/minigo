package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

func main() {
	root := os.Args[1]
	funcs := template.FuncMap{}
	for _, n := range strings.Fields("dict genParamArgs genParamTypes genParamNames genPathString swaggerUriToIrisUri swaggerUriToEchoUri swaggerUriToFiberUri swaggerUriToChiUri swaggerUriToGinUri swaggerUriToGorillaUri swaggerUriToStdHttpUri lcFirst ucFirst ucFirstWithPkgName camelCase genResponsePayload genResponseTypeName genResponseUnmarshal getConditionOfResponseName responsesWithHeaders getResponseTypeDefinitions toStringArray lower title stripNewLines sanitizeGoIdentity schemaNameToTypeName toGoString toGoComment unionTypes withoutPkgName genServerURLWithVariablesFunctionParams httpMethodConstant opts") {
		funcs[n] = func(args ...any) string { return "" }
	}
	t0 := time.Now()
	t := template.New("oapi-codegen").Funcs(funcs)
	var hooks []string
	nbytes := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		buf, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if d.Name() == "hooks.tmpl" {
			hooks = append(hooks, string(buf))
			return nil
		}
		nbytes += len(buf)
		_, err = t.New(strings.TrimPrefix(path, root)).Parse(string(buf))
		return err
	})
	if err != nil {
		panic(err)
	}
	t1 := time.Now()
	for _, h := range hooks {
		c, err := t.Clone()
		if err != nil {
			panic(err)
		}
		if _, err := c.Parse(h); err != nil {
			panic(err)
		}
	}
	t2 := time.Now()
	// A feasibility probe: recreate a namespace from copies of script trees.
	// This excludes serialization and is not a general Parse replacement.
	copyStart := time.Now()
	restored := template.New("oapi-codegen").Funcs(funcs)
	for _, original := range t.Templates() {
		if _, err := restored.AddParseTree(original.Name(), original.Tree.Copy()); err != nil {
			panic(err)
		}
	}
	copyElapsed := time.Since(copyStart)
	for _, original := range t.Templates() {
		got := restored.Lookup(original.Name())
		if got == nil || got.Tree.Root.String() != original.Tree.Root.String() {
			panic("restored tree mismatch: " + original.Name())
		}
	}
	fmt.Printf("copy+install=%.3fs restored=%d\n", copyElapsed.Seconds(), len(restored.Templates()))
	fmt.Printf("bytes=%d templates=%d hooks=%d parse=%.3fs clone+hooks=%.3fs\n", nbytes, len(t.Templates()), len(hooks), t1.Sub(t0).Seconds(), t2.Sub(t1).Seconds())
}
