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
