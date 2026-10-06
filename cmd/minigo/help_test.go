package main

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestHelpTopics(t *testing.T) {
	var got []string
	for k := range helps {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"", "gen-intrinsics", "repl", "run", "vet"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("help topics mismatch (-want +got):\n%s", diff)
	}
}

func TestSubcommandArgs(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func() error
		want string // "help", "usage", or "" for neither
	}{
		{"run -h", func() error { return run(ctx, []string{"-h"}) }, "help"},
		{"run ref --help", func() error { return run(ctx, []string{"./x", "--help"}) }, "help"},
		{"run", func() error { return run(ctx, nil) }, "usage"},
		{"run unknown flag", func() error { return run(ctx, []string{"./x", "-x"}) }, "usage"},
		{"run --entry", func() error { return run(ctx, []string{"./x", "--entry"}) }, "usage"},
		{"vet -h", func() error { return vet(ctx, []string{"-h"}) }, "help"},
		{"vet", func() error { return vet(ctx, nil) }, "usage"},
		{"vet two refs", func() error { return vet(ctx, []string{"a", "b"}) }, "usage"},
		{"gen-intrinsics -h", func() error { return genIntrinsics(ctx, []string{"-h"}) }, "help"},
		{"gen-intrinsics", func() error { return genIntrinsics(ctx, nil) }, "usage"},
		{"repl -h", func() error { return replArgs([]string{"-h"}) }, "help"},
		{"repl x", func() error { return replArgs([]string{"x"}) }, "usage"},
		{"repl", func() error { return replArgs(nil) }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			got := ""
			switch {
			case errors.Is(err, errHelp):
				got = "help"
			case errors.As(err, new(*usageError)):
				got = "usage"
			case err != nil:
				got = "other: " + err.Error()
			}
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Errorf("error kind mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
