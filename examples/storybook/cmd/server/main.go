// Command server serves the example application's user pages with net/http.
package main

import (
	"context"
	"flag"
	"html/template"
	"log/slog"
	"net/http"
	"os"

	"github.com/podhmo/minigo/examples/storybook/app"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	flag.Parse()
	h := &app.Handler{Store: app.MemoryStore{
		"1": {ID: "1", Name: "Alice", Tags: []string{"admin", "go"}, Badge: template.HTML(`<b class="badge">staff</b>`)},
		"2": {ID: "2", Name: "<script>alert(1)</script>"},
		"3": {ID: "3", Name: "Bob", Tags: []string{"go"}},
	}}
	ctx := context.Background()
	slog.InfoContext(ctx, "serving users", "addr", *addr)
	if err := http.ListenAndServe(*addr, h.Routes()); err != nil {
		slog.ErrorContext(ctx, "serve", "err", err)
		os.Exit(1)
	}
}
