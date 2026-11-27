package minigo

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestReplaceSourceFuncScriptReplacement(t *testing.T) {
	ctx := context.Background()
	e := NewEngine(".")
	fake, err := e.Member(ctx, "./testdata/srcreplace/fake", "LoadUser")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.ReplaceSourceFunc(ctx, "./testdata/srcreplace/app", "LoadUser", fake); err != nil {
		t.Fatal(err)
	}
	got, err := e.Run(ctx, "./testdata/srcreplace/main", "Render")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("fake/fake", got); diff != "" {
		t.Fatalf("Render() mismatch (-want +got):\n%s", diff)
	}
	got, err = e.Run(ctx, "./testdata/srcreplace/main", "Direct")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("fake", got); diff != "" {
		t.Fatalf("Direct() mismatch (-want +got):\n%s", diff)
	}

	s := e.NewSession()
	got, err = s.Run(ctx, "./testdata/srcreplace/main", "Render")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("fake/fake", got); diff != "" {
		t.Fatalf("session Render() mismatch (-want +got):\n%s", diff)
	}
}

func TestReplaceSourceFuncGoReplacement(t *testing.T) {
	ctx := context.Background()
	e := NewEngine(".")
	replacement := func(id int) (int, error) { return 42, nil }
	if err := e.ReplaceSourceFunc(ctx, "./testdata/srcreplace/app", "LoadScore", replacement); err != nil {
		t.Fatal(err)
	}
	got, err := e.Run(ctx, "./testdata/srcreplace/app", "Score")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(42), got); diff != "" {
		t.Fatalf("Score() mismatch (-want +got):\n%s", diff)
	}
}

func TestReplaceSourceFuncRejectsBadSignature(t *testing.T) {
	e := NewEngine(".")
	err := e.ReplaceSourceFunc(context.Background(), "./testdata/srcreplace/app", "LoadScore", func(string) int { return 0 })
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("want signature error, got %v", err)
	}
}

func TestReplaceSourceFuncRejectsAfterInitialization(t *testing.T) {
	ctx := context.Background()
	e := NewEngine(".")
	if _, err := e.Run(ctx, "./testdata/srcreplace/app", "Render"); err != nil {
		t.Fatal(err)
	}
	err := e.ReplaceSourceFunc(ctx, "./testdata/srcreplace/app", "LoadUser", func(int) any { return nil })
	if err == nil || !strings.Contains(err.Error(), "already initializing") {
		t.Fatalf("want initialization error, got %v", err)
	}
}
