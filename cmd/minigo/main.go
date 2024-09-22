package main

import (
	"context"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"log/slog"
	"os"

	"github.com/podhmo/minigo/internal/interpreter"
)

func main() {
	var options struct {
		entrypoint string
	}

	flag.StringVar(&options.entrypoint, "entrypoint", "main", "entrypoint function name")
	flag.Parse()

	if len(flag.Args()) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: minigo <filename>")
		return
	}

	ctx := context.Background()
	// logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	filename := flag.Args()[0]
	if err := run(ctx, filename, options.entrypoint); err != nil {
		logger.ErrorContext(ctx, "failed to run", "error", err)
		return
	}
}

func run(ctx context.Context, filename string, entryPoint string) error {
	fset := token.NewFileSet()

	node, err := parser.ParseFile(fset, filename, nil, parser.AllErrors)
	if err != nil {
		return fmt.Errorf("failed to parse file: %w", err)
	}

	app := interpreter.New(fset, entryPoint, os.Stdout, os.Stderr)
	return app.RunFile(ctx, node)
}
