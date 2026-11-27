package flags

import (
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
