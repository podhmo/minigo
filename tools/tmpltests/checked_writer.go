package main

import (
	"io"
	"sync"
)

// checkedWriter keeps the first write failure from report emission: a
// full or disappearing report file must fail the harness instead of
// leaving a truncated report behind a successful exit.
type checkedWriter struct {
	w   io.Writer
	mu  sync.Mutex
	err error
}

func (w *checkedWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if err != nil {
		w.mu.Lock()
		if w.err == nil {
			w.err = err
		}
		w.mu.Unlock()
	}
	return n, err
}

func (w *checkedWriter) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}
