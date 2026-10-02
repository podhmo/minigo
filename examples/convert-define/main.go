package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"go/scanner"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/podhmo/minigo/examples/convert-define/generator"
	"github.com/podhmo/minigo/examples/convert-define/internal"
)

func main() {
	var (
		defineFile = flag.String("file", "", "path to the go file with conversion definitions")
		output     = flag.String("output", "generated.go", "output file name")
		dryRun     = flag.Bool("dry-run", false, "don't write files, just print to stdout")
		buildTags  = flag.String("tags", "", "build constraint expression written as the generated file's //go:build line")
		strict     = flag.Bool("strict", false, "fail instead of writing output when a field pair would not compile (generation warnings become errors)")
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

	if err := run(ctx, *defineFile, *output, *dryRun, *buildTags, *strict); err != nil {
		// The error is a multi-line, user-facing report; print it as
		// is rather than as an escaped log attribute.
		slog.ErrorContext(ctx, "convert-define failed")
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, defineFile, output string, dryRun bool, buildTags string, strict bool) error {
	header, err := buildConstraintHeader(buildTags)
	if err != nil {
		return err
	}

	if err := checkDefineFile(defineFile); err != nil {
		return err
	}

	slog.InfoContext(ctx, "Starting parser", "file", defineFile)

	runner, err := internal.NewRunner()
	if err != nil {
		return fmt.Errorf("failed to create interpreter runner: %w", err)
	}

	if err := runner.Run(ctx, defineFile); err != nil {
		var list scanner.ErrorList
		if errors.As(err, &list) {
			if src, rerr := os.ReadFile(defineFile); rerr == nil {
				return defineSyntaxError(defineFile, list, src)
			}
		}
		var de *internal.DefineError
		if errors.As(err, &de) {
			src, _ := os.ReadFile(de.Pos.Filename)
			return &dslError{de: de, src: src}
		}
		return fmt.Errorf("failed to run definition script: %w", err)
	}

	// Set the package name after running, so the file has been parsed.
	runner.Info.PackageName = runner.PackageName()

	slog.InfoContext(ctx, "Successfully parsed define file", "parsed_info", runner.Info)

	generatedCode, err := generator.Generate(runner.TypeResolver(), runner.Info, generator.Options{
		Header: header,
		// The generated file joins the package in the output directory;
		// its temporaries must not shadow that package's identifiers.
		PackageIdents: generator.PackageIdents(ctx, filepath.Dir(output)),
		Strict:        strict,
	})
	if err != nil {
		var we *generator.WarningsError
		if errors.As(err, &we) {
			return we // already a complete, user-facing report
		}
		return fmt.Errorf("failed to generate code: %w", err)
	}

	slog.DebugContext(ctx, "Writing output", "file", output)
	formatted, err := formatCode(ctx, output, generatedCode)
	if err != nil {
		// Writing the unformatted code would turn a generator failure
		// into a later, unrelated-looking compile error; fail here.
		slog.DebugContext(ctx, "unformatted generated source", "source", string(generatedCode))
		return fmt.Errorf("formatting %s: %w", output, err)
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
