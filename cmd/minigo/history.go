// Persistent REPL history: a term.History backed by a file
// (~/.minigo_history). Entries load at startup so Up/Down recall
// crosses sessions; each accepted line appends to the file.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// fileHistory implements term.History (index 0 = most recent) over a
// newline-separated file. File errors are ignored — history is
// best-effort, the in-memory list always works.
type fileHistory struct {
	path    string
	entries []string // chronological: oldest first
	max     int
}

// newFileHistory loads path (missing file is fine) and keeps the last
// max entries — rewriting the file down to the tail when it overflowed.
func newFileHistory(path string, max int) *fileHistory {
	h := &fileHistory{path: path, max: max}
	data, err := os.ReadFile(path)
	if err != nil {
		return h
	}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "" {
			h.entries = append(h.entries, line)
		}
	}
	if max > 0 && len(h.entries) > max {
		h.entries = h.entries[len(h.entries)-max:]
		h.rewrite()
	}
	return h
}

// defaultHistoryPath is ~/.minigo_history — a plain dotfile like
// .python_history, deliberately not XDG state so it is easy to find.
func defaultHistoryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".minigo_history"), nil
}

func (h *fileHistory) Len() int { return len(h.entries) }

// At indexes from most recent: At(0) is the last line entered.
func (h *fileHistory) At(idx int) string {
	return h.entries[len(h.entries)-1-idx]
}

func (h *fileHistory) Add(entry string) {
	if entry == "" {
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == entry {
		return // consecutive duplicates are noise, not history
	}
	h.entries = append(h.entries, entry)
	if h.max > 0 && len(h.entries) > h.max {
		h.entries = h.entries[1:]
		h.rewrite()
		return
	}
	if f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		defer f.Close()
		fmt.Fprintln(f, entry)
	}
}

func (h *fileHistory) rewrite() {
	f, err := os.OpenFile(h.path, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	for _, e := range h.entries {
		fmt.Fprintln(f, e)
	}
}
