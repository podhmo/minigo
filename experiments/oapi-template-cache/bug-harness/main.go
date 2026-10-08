// Command prof runs cmd/oapi-codegen under the minigo engine with
// runtime/pprof enabled: prof -C dir -out prefix [-n rounds] -- ARGS
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/pprof"
	"strconv"
	"strings"

	"time"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/runtime"
	"golang.org/x/tools/imports"
)

const pkg = "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"

func main() {
	bindIntSize := flag.Bool("bind-intsize", false, "diagnostic workaround for an unbound constant")
	dir := flag.String("C", ".", "example dir")
	out := flag.String("out", "", "profile prefix (empty: no profile)")
	userCache := flag.String("user-cache-dir", "", "override os.UserCacheDir for a pinned imports index")
	target := flag.String("pkg", pkg, "interpreted package")
	show := flag.Bool("show-output", false, "show script output")
	n := flag.Int("n", 1, "rounds (in-process)")
	nativeImports := flag.Bool("native-imports", true, "bind golang.org/x/tools/imports.Process natively")
	flag.Parse()
	if err := os.Chdir(*dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cwd, _ := os.Getwd()
	modes := map[string]minigo.PackageMode{}
	for _, p := range strings.Split("text/template,encoding/json,encoding/hex,encoding/base64,encoding/base32,slices,maps", ",") {
		modes[p] = minigo.ModeSource
	}
	if *out != "" {
		f, _ := os.Create(*out + ".cpu.pprof")
		defer f.Close()
		pprof.StartCPUProfile(f)
	}
	for i := 0; i < *n; i++ {
		t0 := time.Now()
		var output io.Writer = io.Discard
		if *show {
			output = os.Stdout
		}
		e := minigo.NewEngine(cwd, minigo.WithOutput(output), minigo.WithArgs(append([]string{*target}, flag.Args()...)), minigo.WithPackageModes(modes))
		if *bindIntSize {
			p, err := e.Package(context.Background(), "strconv")
			if err != nil {
				panic(err)
			}
			p.Globals.Set("IntSize", int64(strconv.IntSize))
		}
		if *userCache != "" {
			boundOS, err := e.Package(context.Background(), "os")
			if err != nil {
				panic(err)
			}
			boundOS.Globals.Set("UserCacheDir", &runtime.GoValue{V: func() (string, error) { return *userCache, nil }})
		}
		if *nativeImports {
			e.Bind("golang.org/x/tools/imports", map[string]runtime.Value{
				"Process": &runtime.GoValue{V: imports.Process},
			})
		}
		_, err := e.Run(context.Background(), *target, "")
		fmt.Printf("elapsed=%.3fs err=%v\n", time.Since(t0).Seconds(), err)
		if err != nil {
			os.Exit(1)
		}
	}
	if *out != "" {
		pprof.StopCPUProfile()
		if f, err := os.Create(*out + ".allocs.pprof"); err == nil {
			pprof.Lookup("allocs").WriteTo(f, 0)
			f.Close()
		}
	}
}
