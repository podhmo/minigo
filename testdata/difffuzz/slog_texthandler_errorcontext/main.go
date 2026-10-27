package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"
)

// Buffer mirrors log/slog/internal/buffer: a named []byte whose pool
// New hands back (*Buffer)(&b). The pool must keep the script type so
// Get().(*Buffer) holds.
type Buffer []byte

var bufPool = sync.Pool{New: func() any {
	b := make([]byte, 0, 8)
	return (*Buffer)(&b)
}}

// timeLocation mirrors slog's `type timeLocation *time.Location`.
type timeLocation *time.Location

type point struct{ X, Y int }

func (p point) LogValue() slog.Value {
	return slog.GroupValue(slog.Int("x", p.X), slog.Int("y", p.Y))
}

func pools() {
	b := bufPool.Get().(*Buffer)
	*b = append(*b, "hi"...)
	fmt.Printf("%T %q %d\n", b, *b, cap(*b))

	var p sync.Pool
	p.New = func() any { return 7 }
	fmt.Printf("%T %v\n", p.Get(), p.Get())
	p.New = func() any { return new(bytes.Buffer) }
	fmt.Printf("%T\n", p.Get())
}

func conversions() {
	var a any = timeLocation(time.UTC)
	fmt.Printf("%T\n", a)
	l, ok := a.(timeLocation)
	fmt.Println(ok, (*time.Location)(l) == time.UTC, (*time.Location)(l).String())
}

func grow() {
	s := []int{1, 2}
	g := slices.Grow(s, 10)
	fmt.Println(g, len(g), cap(g) >= 12, g[:cap(g)][5])
	var n []int
	fmt.Println(slices.Grow(n, 0) == nil, cap(slices.Grow(n, 3)) >= 3)
	c := slices.Clip(make([]int, 1, 5))
	fmt.Println(len(c), cap(c))
	// gc grows through append: cap follows append's growth rule, and
	// the old spare keeps its contents.
	fmt.Println(cap(slices.Grow(make([]int, 5), 1)), cap(slices.Grow(make([]int, 5), 20)))
	fmt.Println(cap(slices.Grow([]string{"a"}, 3)))
	old := []int{1, 2, 3}[:1]
	h := slices.Grow(old, 5)
	fmt.Println(h, h[:cap(h)][:3], h[:cap(h)][cap(h)-1])
	type Ints []int
	fmt.Printf("%T %d\n", slices.Grow(Ints{1}, 2), cap(slices.Grow(Ints{1}, 2)))
}

func logs() {
	ctx := context.Background()
	drop := func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey && len(groups) == 0 {
			return slog.Attr{}
		}
		return a
	}
	opts := &slog.HandlerOptions{ReplaceAttr: drop, Level: slog.LevelDebug}
	l := slog.New(slog.NewTextHandler(os.Stdout, opts))
	l.ErrorContext(ctx, "error ctx", "k", 1)
	l.Error("error", "err", errors.New("boom"))
	l.Info("info", "s", "a b", "f", 1.5, "b", true)
	l.InfoContext(ctx, "info ctx", slog.Duration("d", 3*time.Second))
	l.Warn("warn", "u", uint64(7))
	l.WarnContext(ctx, "warn ctx")
	l.Debug("debug", "p", point{1, 2})
	l.DebugContext(ctx, "debug ctx", "t", time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC))
	l.Log(ctx, slog.LevelWarn+1, "log", "n", nil)
	l.LogAttrs(ctx, slog.LevelInfo, "logattrs", slog.String("a", "b"), slog.Group("g", slog.Int("i", 1)))
	w := l.With("req", 42).WithGroup("grp")
	w.Info("with", "x", 1, "err", fmt.Errorf("wrap: %w", errors.New("inner")))
	w.With("y", 2).ErrorContext(ctx, "nested")
	l.Info("any", slog.Any("m", map[string]int{"a": 1}), slog.Any("sl", []int{1, 2}))
}

func main() {
	pools()
	conversions()
	grow()
	logs()
}
