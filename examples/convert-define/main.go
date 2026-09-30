package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/podhmo/minigo/examples/convert-define/generator"
	"github.com/podhmo/minigo/examples/convert-define/internal"
	goscan "github.com/podhmo/minigo/examples/convert-define/pkg/goscan"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scanner"
	"golang.org/x/tools/imports"
)

func main() {
	var (
		defineFile = flag.String("file", "", "path to the go file with conversion definitions")
		output     = flag.String("output", "generated.go", "output file name")
		dryRun     = flag.Bool("dry-run", false, "don't write files, just print to stdout")
		buildTags  = flag.String("tags", "", "build tags to use when running the code generator")
		logLevel   = slog.LevelWarn
	)
	flag.TextVar(&logLevel, "log-level", &logLevel, "set log level (debug, info, warn, error)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: convert-define -file <definitions.go> [-output <filename>]\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *defineFile == "" {
		flag.Usage()
		os.Exit(1)
	}

	opts := slog.HandlerOptions{Level: &logLevel}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &opts))
	slog.SetDefault(logger)

	ctx := context.Background()

	if err := run(ctx, *defineFile, *output, *dryRun, *buildTags); err != nil {
		slog.ErrorContext(ctx, "Error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(ctx context.Context, defineFile, output string, dryRun bool, buildTags string) error {
	slog.InfoContext(ctx, "Starting parser", "file", defineFile)

	// Add overrides for standard library types that cause scanning issues.
	overrides := scanner.ExternalTypeOverride{
		"time.Time": &scanner.TypeInfo{
			Name:    "Time",
			PkgPath: "time",
			Kind:    scanner.StructKind,
		},
		"*time.Time": &scanner.TypeInfo{
			Name:    "Time",
			PkgPath: "time",
			Kind:    scanner.StructKind,
		},
	}
	runner, err := internal.NewRunner(
		goscan.WithGoModuleResolver(),
		goscan.WithExternalTypeOverrides(overrides),
	)
	if err != nil {
		return fmt.Errorf("failed to create interpreter runner: %w", err)
	}

	if err := runner.Run(ctx, defineFile); err != nil {
		return fmt.Errorf("failed to run definition script: %w", err)
	}

	// Set the package name after running, so the file has been parsed.
	runner.Info.PackageName = runner.PackageName()

	slog.InfoContext(ctx, "Successfully parsed define file", "parsed_info", runner.Info)

	header := ""
	if buildTags != "" {
		header = fmt.Sprintf("\n//go:build %s\n// +build %s\n\n", buildTags, buildTags)
	}
	generatedCode, err := generator.Generate(runner.Scanner(), runner.Info, header)
	if err != nil {
		return fmt.Errorf("failed to generate code: %w", err)
	}

	slog.DebugContext(ctx, "Writing output", "file", output)
	formatted, err := formatCode(ctx, output, generatedCode)
	if err != nil {
		slog.WarnContext(ctx, "code formatting failed, using unformatted code", "error", err)
		formatted = generatedCode // Use unformatted code on format error
	}

	if dryRun {
		slog.InfoContext(ctx, "Dry run: skipping file write", "path", output)
		fmt.Fprintf(os.Stdout, "---\n// file: %s\n---\n", output)
		os.Stdout.Write(formatted)
	} else {
		if err := os.WriteFile(output, formatted, 0644); err != nil {
			return fmt.Errorf("failed to write formatted code to %s: %w", output, err)
		}
	}

	slog.InfoContext(ctx, "Successfully generated skeleton file", "output", output)
	return nil
}

func formatCode(ctx context.Context, filename string, src []byte) ([]byte, error) {
	formatted, err := imports.Process(filename, src, nil)
	if err != nil {
		return nil, fmt.Errorf("goimports failed: %w", err)
	}
	return formatted, nil
}
