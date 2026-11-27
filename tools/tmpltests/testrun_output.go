package main

import (
	"io"
	"os"
)

// testRunOutput mirrors verbose output to stdout and, when report is set,
// to the report file the process runner would have written.
func testRunOutput(report string) (io.Writer, *os.File, error) {
	out := io.Writer(os.Stdout)
	if report == "" {
		return out, nil, nil
	}
	f, err := os.Create(report)
	if err != nil {
		return nil, nil, err
	}
	return io.MultiWriter(os.Stdout, f), f, nil
}
