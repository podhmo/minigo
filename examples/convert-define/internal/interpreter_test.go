package internal

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	goscan "github.com/podhmo/go-scan"
	"github.com/podhmo/go-scan/examples/convert/model"
)

func TestParser(t *testing.T) {
	ctx := context.Background()
	inputFile := filepath.Join("../testdata", "mappings.go")

	// The runner needs a correctly configured scanner to find all the packages.
	// Overrides are no longer needed.
	runner, err := NewRunner(
		goscan.WithGoModuleResolver(),
	)
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

	// Check field tags for user
	userSrcInfo, ok := info.Structs["SrcUser"]
	if !ok {
		t.Fatal("SrcUser info not found")
	}

	idTag := findField(t, userSrcInfo, "ID").Tag
	if want, got := "UserID", idTag.DstFieldName; want != got {
		t.Errorf("ID tag DstFieldName: want %q, got %q", want, got)
	}
	if want, got := "", idTag.UsingFunc; want != got {
		t.Errorf("ID tag UsingFunc: want %q, got %q", want, got)
	}

	contactTag := findField(t, userSrcInfo, "ContactInfo").Tag
	if want, got := "Contact", contactTag.DstFieldName; want != got {
		t.Errorf("ContactInfo tag DstFieldName: want %q, got %q", want, got)
	}
	if want, got := "funcs.ConvertSrcContactToDstContact", contactTag.UsingFunc; want != got {
		t.Errorf("ContactInfo tag UsingFunc: want %q, got %q", want, got)
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

	// Check field tags for address
	addrSrcInfo, ok := info.Structs["SrcAddress"]
	if !ok {
		t.Fatal("SrcAddress info not found")
	}

	streetTag := findField(t, addrSrcInfo, "Street").Tag
	if want, got := "FullStreet", streetTag.DstFieldName; want != got {
		t.Errorf("Street tag DstFieldName: want %q, got %q", want, got)
	}
	cityTag := findField(t, addrSrcInfo, "City").Tag
	if want, got := "CityName", cityTag.DstFieldName; want != got {
		t.Errorf("City tag DstFieldName: want %q, got %q", want, got)
	}
}

func findField(t *testing.T, structInfo *model.StructInfo, name string) model.FieldInfo {
	t.Helper()
	for _, f := range structInfo.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("field %q not found in struct %s", name, structInfo.Name)
	return model.FieldInfo{}
}

func TestRunner(t *testing.T) {
	wd := filepath.Join("..", "testdata", "success")

	runner, err := NewRunner(
		goscan.WithWorkDir(wd),
		goscan.WithGoModuleResolver(),
	)
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
