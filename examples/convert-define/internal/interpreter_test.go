package internal

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/examples/convert-define/model"
)

func TestParser(t *testing.T) {
	ctx := context.Background()
	inputFile := filepath.Join("../testdata", "mappings.go")

	// The engine anchors at the define file's directory, so its module
	// context governs package resolution — no scanner setup needed.
	runner, err := NewRunner()
	if err != nil {
		t.Fatalf("NewRunner() failed: %+v", err)
	}

	if err := runner.Run(ctx, inputFile); err != nil {
		t.Fatalf("runner.Run() failed: %+v", err)
	}

	info := runner.Info
	if info == nil {
		t.Fatal("runner.Info is nil")
	}

	// Check global rules
	if want, got := 2, len(info.GlobalRules); want != got {
		t.Fatalf("expected %d global rules, but got %d", want, got)
	}
	if want, got := "time.Time", info.GlobalRules[0].SrcTypeName; want != got {
		t.Errorf("GlobalRules[0].SrcTypeName: want %q, got %q", want, got)
	}
	if want, got := "*time.Time", info.GlobalRules[1].SrcTypeName; want != got {
		t.Errorf("GlobalRules[1].SrcTypeName: want %q, got %q", want, got)
	}

	// Check conversion pairs
	if want, got := 2, len(info.ConversionPairs); want != got {
		t.Fatalf("expected %d conversion pairs, but got %d", want, got)
	}

	// -- Pair 1: SrcUser -> DstUser
	userPair := info.ConversionPairs[0]
	if want, got := "SrcUser", userPair.SrcTypeName; want != got {
		t.Errorf("userPair.SrcTypeName: want %q, got %q", want, got)
	}
	if want, got := "DstUser", userPair.DstTypeName; want != got {
		t.Errorf("userPair.DstTypeName: want %q, got %q", want, got)
	}

	// Check computed fields for user
	if want, got := 1, len(userPair.Computed); want != got {
		t.Fatalf("expected %d computed field for user, but got %d", want, got)
	}
	if want, got := "FullName", userPair.Computed[0].DstName; want != got {
		t.Errorf("userPair.Computed[0].DstName: want %q, got %q", want, got)
	}
	{
		want := "funcs.MakeFullName(src.FirstName, src.LastName)"
		got := userPair.Computed[0].Expr
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("userPair.Computed[0].Expr mismatch (-want +got):\n%s", diff)
		}
	}

	// Explicit mappings land on the pair, in DSL call order.
	if userPair.Mapping == nil {
		t.Fatal("userPair.Mapping is nil")
	}
	wantUserMaps := []model.FieldMap{
		{SrcName: "ID", DstName: "UserID"},
		{SrcName: "ContactInfo", DstName: "Contact", Converter: "funcs.ConvertSrcContactToDstContact"},
	}
	if diff := cmp.Diff(wantUserMaps, userPair.Mapping.Maps); diff != "" {
		t.Errorf("userPair.Mapping.Maps mismatch (-want +got):\n%s", diff)
	}

	// -- Pair 2: SrcAddress -> DstAddress
	addrPair := info.ConversionPairs[1]
	if want, got := "SrcAddress", addrPair.SrcTypeName; want != got {
		t.Errorf("addrPair.SrcTypeName: want %q, got %q", want, got)
	}
	if want, got := "DstAddress", addrPair.DstTypeName; want != got {
		t.Errorf("addrPair.DstTypeName: want %q, got %q", want, got)
	}
	if want, got := 0, len(addrPair.Computed); want != got {
		t.Fatalf("address pair should have no computed fields, but got %d", got)
	}

	if addrPair.Mapping == nil {
		t.Fatal("addrPair.Mapping is nil")
	}
	wantAddrMaps := []model.FieldMap{
		{SrcName: "Street", DstName: "FullStreet"},
		{SrcName: "City", DstName: "CityName"},
	}
	if diff := cmp.Diff(wantAddrMaps, addrPair.Mapping.Maps); diff != "" {
		t.Errorf("addrPair.Mapping.Maps mismatch (-want +got):\n%s", diff)
	}
}

func TestRunner(t *testing.T) {
	wd := filepath.Join("..", "testdata", "success")

	runner, err := NewRunner()
	if err != nil {
		t.Fatalf("NewRunner() failed: %+v", err)
	}

	defineFile := filepath.Join(wd, "define.go")
	if err := runner.Run(context.Background(), defineFile); err != nil {
		t.Fatalf("Run() failed: %+v", err)
	}

	if got, want := len(runner.Info.GlobalRules), 1; got != want {
		t.Fatalf("expected %d global rule, but got %d", want, got)
	}

	rule := runner.Info.GlobalRules[0]
	want := "time.Time"
	if got := rule.SrcTypeName; got != want {
		t.Errorf("SrcTypeName: want %q, got %q", want, got)
	}
	if rule.SrcTypeInfo == nil {
		t.Fatal("SrcTypeInfo should not be nil")
	}
	if got, want := rule.SrcTypeInfo.Name, "Time"; got != want {
		t.Errorf("SrcTypeInfo.Name: want %q, got %q", want, got)
	}

	want = "string"
	if got := rule.DstTypeName; got != want {
		t.Errorf("DstTypeName: want %q, got %q", want, got)
	}
	if rule.DstTypeInfo != nil {
		t.Errorf("DstTypeInfo should be nil for builtin string, but was not: %v", rule.DstTypeInfo)
	}

	want = "convutil.TimeToString"
	if got := rule.UsingFunc; got != want {
		t.Errorf("UsingFunc: want %q, got %q", want, got)
	}

	wantImports := map[string]string{
		"convutil": "example.com/test/convutil",
	}
	if diff := cmp.Diff(wantImports, runner.Info.Imports); diff != "" {
		t.Errorf("Imports mismatch (-want +got):\n%s", diff)
	}
}
