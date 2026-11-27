package main

import (
	"io"
	"os"
)

// testRunOutput mirrors verbose output to stdout and, when report is set,
// to the report file the process runner would have written.
func testRunOutput(report string) (*checkedWriter, *os.File, error) {
	out := io.Writer(os.Stdout)
	if report == "" {
		return &checkedWriter{w: out}, nil, nil
	}
	f, err := os.Create(report)
	if err != nil {
		return nil, nil, err
	}
	return &checkedWriter{w: io.MultiWriter(os.Stdout, f)}, f, nil
}
