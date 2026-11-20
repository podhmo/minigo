package main

import (
	"fmt"
	"os"
)

func onlyText(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b) // hint: b only read as text
}

func alsoLen(p string) int {
	b, _ := os.ReadFile(p)
	if len(b) == 0 {
		return -1
	}
	return len(string(b)) // hint: len and string both count as text reads
}

func passedOn(p string) string {
	b, _ := os.ReadFile(p)
	return fmt.Sprint(b) // no hint: a foreign call sees the bytes
}

func indexed(p string) byte {
	b, _ := os.ReadFile(p)
	return b[0] // no hint: the script inspects the bytes
}

func shadowed(p string) string {
	b, _ := os.ReadFile(p)
	{
		b := "inner" // no hint: the outer b's uses are ambiguous
		_ = b
	}
	return string(b)
}

func unused(p string) error {
	_, err := os.ReadFile(p) // no hint: no byte var to rewrite
	return err
}

func main() {
	_ = onlyText
	_ = alsoLen
	_ = passedOn
	_ = indexed
	_ = shadowed
	_ = unused
}

type holder struct{ b []byte }

func fieldNameSame(p string) (holder, string) {
	var h holder
	b, _ := os.ReadFile(p)
	h.b = []byte{1}     // field name `b` is not a use of the var
	return h, string(b) // hint still fires
}

func shadowImport() int {
	type faker struct{ n int }
	os := faker{n: 7}
	return os.n // `os` rebound: os.ReadFile below is a method call
}

func shadowBuiltin(p string) int {
	string := func(any) string { return "x" }
	b, _ := os.ReadFile(p)
	_ = string(b) // a foreign call sees the bytes — no hint
	return len(b) - 1
}
