// Line editing for `minigo repl`: on a real terminal the input loop
// upgrades to a raw-mode line editor (golang.org/x/term) so cursor keys,
// in-session history and tab completion work. Pipes and tests keep the
// plain bufio.Scanner path — no prompt escape sequences are emitted.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/podhmo/minigo"
	"golang.org/x/term"
)

// replSource yields one input line per call; the prompt is decided by
// the caller (">> " vs the ".. " continuation).
type replSource interface {
	next(prompt string) (string, error)
}

// scannerSource is the plain path: print the prompt, scan a line.
type scannerSource struct {
	sc  *bufio.Scanner
	out io.Writer
}

func (s *scannerSource) next(prompt string) (string, error) {
	fmt.Fprint(s.out, prompt)
	if !s.sc.Scan() {
		if err := s.sc.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return s.sc.Text(), nil
}

// termSource hands the prompt to the line editor and returns edited
// lines. Ctrl-C and Ctrl-D both surface as io.EOF (x/term semantics).
type termSource struct {
	t *term.Terminal
}

func (s *termSource) next(prompt string) (string, error) {
	s.t.SetPrompt(prompt)
	return s.t.ReadLine()
}

// readWriter glues the input file and the output writer into the single
// ReadWriter a Terminal wants.
type readWriter struct {
	io.Reader
	io.Writer
}

// writeWithCRLF turns \n into \r\n — under raw mode a bare \n does not
// return the cursor to column 0, so every message the loop prints
// (and everything the interpreter echoes) needs the translation.
type writeWithCRLF struct {
	w io.Writer
}

func (w writeWithCRLF) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		todo := p
		if i >= 0 {
			todo = p[:i]
		}
		if len(todo) > 0 {
			if _, err := w.w.Write(todo); err != nil {
				return 0, err
			}
		}
		if i < 0 {
			break
		}
		if _, err := w.w.Write([]byte("\r\n")); err != nil {
			return 0, err
		}
		p = p[i+1:]
	}
	return n, nil
}

// metaCommands are the `:`-lines the front-end completes itself — the
// processor-side completer stays out of this namespace.
var metaCommands = []string{
	":cd", ":comp", ":exit", ":help", ":ls",
	":pin", ":q", ":quit", ":reset", ":unpin",
}

// completerFor builds the AutoCompleteCallback: tab splices the common
// extension (or the whole match) into the line; an ambiguous press lists
// the candidates above the repainted prompt.
func completerFor(r *minigo.REPL, t *term.Terminal) func(line string, pos int, key rune) (string, int, bool) {
	return func(line string, pos int, key rune) (string, int, bool) {
		if key != '\t' {
			return "", 0, false
		}
		newLine, newPos, cands, ok := completeSplice(r, line, pos)
		if !ok {
			return "", 0, false
		}
		if len(cands) > 1 {
			t.Write([]byte("\n" + formatCandidates(cands) + "\n"))
		}
		return newLine, newPos, true
	}
}

// completeSplice computes the post-tab line: the shared extension of
// every candidate is inserted (a unique candidate completes fully), and
// the rest of the line past the cursor is preserved.
func completeSplice(r *minigo.REPL, line string, pos int) (newLine string, newPos int, cands []minigo.Candidate, ok bool) {
	head, tail := line[:pos], line[pos:]
	if c, isCmd := strings.CutPrefix(head, ":"); isCmd && !strings.ContainsAny(c, " \t") {
		var names []string
		for _, m := range metaCommands {
			if strings.HasPrefix(m, head) {
				names = append(names, m)
			}
		}
		if len(names) == 0 {
			return "", 0, nil, false
		}
		ext := commonPrefix(names)
		return ext + tail, len(ext), commandCandidates(names), true
	}
	start, cc := r.CompleteToken(head)
	if len(cc) == 0 {
		return "", 0, nil, false
	}
	ext := commonPrefix(candidateNames(cc))
	return head[:start] + ext + tail, start + len(ext), cc, true
}

// commandCandidates renders the meta-command matches as Candidates so
// they list like any other completion.
func commandCandidates(names []string) []minigo.Candidate {
	out := make([]minigo.Candidate, len(names))
	for i, n := range names {
		out[i] = minigo.Candidate{Name: n, Kind: "command"}
	}
	return out
}

func candidateNames(cands []minigo.Candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.Name
	}
	return out
}

func commonPrefix(names []string) string {
	if len(names) == 0 {
		return ""
	}
	p := names[0]
	for _, n := range names[1:] {
		for !strings.HasPrefix(n, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

// formatCandidates renders the ambiguous-press listing: names packed
// into a few columns, detail dropped (the table stays compact — `:comp`
// exists for the verbose view).
func formatCandidates(cands []minigo.Candidate) string {
	const maxRows = 20
	names := candidateNames(cands)
	shown := names
	truncated := 0
	if len(names) > maxRows {
		shown = names[:maxRows]
		truncated = len(names) - maxRows
	}
	width := 0
	for _, n := range shown {
		if len(n) > width {
			width = len(n)
		}
	}
	width += 2
	cols := 1
	if w := 80 / width; w > 1 {
		cols = w
	}
	var b strings.Builder
	for i, n := range shown {
		if i%cols == 0 && i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%-*s", width, n)
	}
	if truncated > 0 {
		fmt.Fprintf(&b, "\n… and %d more", truncated)
	}
	return b.String()
}
