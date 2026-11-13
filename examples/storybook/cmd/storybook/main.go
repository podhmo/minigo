// Command storybook renders "stories" of a net/http application with
// minigo, without building the application.
//
// A story is an exported func StoryXxx() in a file guarded by a build tag (default "story"), so
// the go toolchain never builds it into the real binary. The story calls
// the application's rendering code with dummy values; storybook
// interprets the package with that tag enabled and captures what the
// story prints to stdout. The command is an optional browser/list helper;
// minigo run --tags story ./app StoryUser can call the same story directly.
//
//	storybook -list ./app                 # list stories
//	storybook ./app StoryUser             # print one story's body
//	storybook -o out ./app                # write out/index.html + one page per story
//	storybook -http :6006 ./app           # serve them, re-interpreting on every request
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "storybook: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("storybook", flag.ContinueOnError)
	tag := fs.String("tag", "story", "build tag guarding the story files")
	src := fs.String("src", "", "comma-separated packages to interpret from source (minigo run --src)")
	list := fs.Bool("list", false, "list stories and exit")
	out := fs.String("o", "", "write index.html and one page per story into this directory")
	addr := fs.String("http", "", "serve the stories on this address (e.g. :6006)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: storybook [flags] <pkgdir> [Story]")
	}
	b := &Book{Dir: fs.Arg(0), Tag: *tag}
	if *src != "" {
		b.Src = strings.Split(*src, ",")
	}
	stories, err := b.Stories()
	if err != nil {
		return err
	}
	switch {
	case *list:
		for _, s := range stories {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", s.Name, s.Pos, s.Doc)
		}
		return nil
	case *out != "":
		return b.Export(ctx, *out, stories)
	case *addr != "":
		slog.InfoContext(ctx, "serving stories", "addr", *addr, "dir", b.Dir, "stories", len(stories))
		return http.ListenAndServe(*addr, b.Handler())
	case fs.NArg() >= 2:
		res, err := b.Render(ctx, fs.Arg(1))
		if err != nil {
			return err
		}
		_, err = stdout.Write(res)
		return err
	default:
		return errors.New("one of -list, -o, -http or a story name is required")
	}
}
