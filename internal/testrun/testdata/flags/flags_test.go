package flags

import (
	"errors"
	"flag"
	"testing"
)

var level = flag.Int("level", 7, "test level")
var label string

func init() {
	flag.StringVar(&label, "label", "default", "test label")
	flag.Parse()
}

func TestFlagDefaults(t *testing.T) {
	if *level != 7 {
		t.Errorf("level = %d, want 7", *level)
	}
	if label != "default" {
		t.Errorf("label = %q, want default", label)
	}
	if !flag.Parsed() {
		t.Error("flag.Parsed() = false after init's Parse")
	}
}

func TestShort(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	t.Log("ran long")
}

func TestDeadline(t *testing.T) {
	d, ok := t.Deadline()
	if ok {
		t.Error("Deadline() ok = true without -timeout")
	}
	if !d.IsZero() {
		t.Error("Deadline() returned a non-zero time without -timeout")
	}
}

func TestFlagSetTypes(t *testing.T) {
	if err := flag.Set("level", "8"); err != nil {
		t.Fatalf("flag.Set(level, 8): %v", err)
	}
	if *level != 8 {
		t.Errorf("level = %d after Set, want 8", *level)
	}
	if err := flag.Set("level", "not-an-int"); err == nil {
		t.Error("flag.Set(level, not-an-int) succeeded")
	}
	if *level != 8 {
		t.Errorf("level = %d after a failed Set, want 8", *level)
	}
}

type smallFlag int

func (v *smallFlag) String() string { return "small" }
func (v *smallFlag) Set(s string) error {
	if s == "bad" {
		return errors.New("bad value")
	}
	*v = 1
	return nil
}

func TestCustomFlagSetError(t *testing.T) {
	var v smallFlag
	flag.Var(&v, "small", "small value")
	if err := flag.Set("small", "bad"); err == nil {
		t.Fatal("flag.Set did not return the custom error")
	} else if err.Error() != "bad value" {
		t.Fatalf("flag.Set error = %q, want bad value", err.Error())
	}
	if err := flag.Set("small", "ok"); err != nil {
		t.Fatalf("flag.Set ok: %v", err)
	}
	if v != 1 {
		t.Fatalf("v = %d, want 1", v)
	}
}

func TestNewFlagSet(t *testing.T) {
	fs := flag.NewFlagSet("case", flag.ContinueOnError)
	name := fs.String("name", "default", "name value")
	count := fs.Int("count", 1, "count value")
	verbose := fs.Bool("verbose", false, "verbose value")
	var small smallFlag
	fs.Var(&small, "small", "small value")

	err := fs.Parse([]string{"-name", "bob", "-count=3", "-verbose", "-small", "ok", "rest", "-x"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if *name != "bob" || *count != 3 || !*verbose || small != 1 {
		t.Fatalf("parsed values = %q, %d, %v, %d", *name, *count, *verbose, small)
	}
	if !fs.Parsed() {
		t.Error("Parsed() = false")
	}
	if fs.NArg() != 2 || fs.Arg(0) != "rest" || fs.Arg(1) != "-x" {
		t.Fatalf("positional args = %d %q %q", fs.NArg(), fs.Arg(0), fs.Arg(1))
	}
	if fs.NFlag() != 4 {
		t.Fatalf("NFlag = %d, want 4", fs.NFlag())
	}
	if fs.Lookup("name") == nil || fs.Lookup("missing") != nil {
		t.Error("Lookup did not distinguish defined and missing flags")
	}
	if err := fs.Set("name", "ann"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if *name != "ann" {
		t.Fatalf("name after Set = %q", *name)
	}
	if fs.NFlag() != 4 {
		t.Fatalf("NFlag after setting the same flag = %d, want 4", fs.NFlag())
	}
	if err := fs.Parse([]string{"-missing"}); err == nil {
		t.Fatal("Parse accepted an undefined flag")
	}
}
