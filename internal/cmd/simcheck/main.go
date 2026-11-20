// simcheck is the similarity ratchet: it fails when the tree gains
// duplicate code the baseline does not know about.
//
// Two detectors run:
//
//   - function pairs: gosimilarity's JSON output (--pairs) is diffed
//     against the baseline's recorded pair set.
//   - fragment windows: an in-process matcher hashes normalized
//     8-line windows and flags the same content appearing twice —
//     the in-function fragments gosimilarity misses.
//
// Refresh the baseline with -write-baseline after a legitimate
// duplication is reviewed.
//
//	go run ./internal/cmd/simcheck -pairs /tmp/gsim.json
//	go run ./internal/cmd/simcheck -write-baseline -pairs /tmp/gsim.json
package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const defaultBaseline = "internal/archcheck/similarity-baseline.json"

// baseline pins the known duplicates: gosimilarity pair ids
// (fileA:nameA|fileB:nameB, sorted) and window pairs
// (hash@fileA|fileB).
type baseline struct {
	Pairs   []string `json:"pairs"`
	Windows []string `json:"windows"`
}

type gsOutput struct {
	Pairs        []gsPair `json:"pairs"`
	Declarations []gsDecl `json:"declarations"`
}

type gsPair struct {
	A string `json:"a"`
	B string `json:"b"`
}

type gsDecl struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	File string `json:"file"`
}

var skipDirs = map[string]bool{
	".git": true, ".claude": true, "testdata": true, "examples": true,
	"tools": true, "docs": true, "vendor": true, "node_modules": true,
}

func normLines(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.Join(strings.Fields(l), " ")
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// trivialWindow skips windows that are only delimiters — `}` runs and
// similar hash-collide everywhere without meaning anything.
func trivialWindow(ls []string) bool {
	for _, l := range ls {
		switch l {
		case "}", "{", ")", "]", ",", "};", "})", "},":
		default:
			return false
		}
	}
	return true
}

// scanWindows collects the repo's 8-line window pairs: hash -> the
// unordered file pairs that share it.
func scanWindows(root string, size int) ([]string, error) {
	byHash := map[string]map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		ls := normLines(path)
		for i := 0; i+size <= len(ls); i++ {
			w := ls[i : i+size]
			if trivialWindow(w) {
				continue
			}
			h := sha1.Sum([]byte(strings.Join(w, "\n")))
			k := hex.EncodeToString(h[:])
			if byHash[k] == nil {
				byHash[k] = map[string]bool{}
			}
			byHash[k][rel] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for h, files := range byHash {
		if len(files) < 2 {
			continue
		}
		var fs2 []string
		for f := range files {
			fs2 = append(fs2, f)
		}
		sort.Strings(fs2)
		for i := 0; i < len(fs2); i++ {
			for j := i + 1; j < len(fs2); j++ {
				out = append(out, h+"@"+fs2[i]+"|"+fs2[j])
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// pairIDs normalizes gosimilarity's pairs to "file:name|file:name".
func pairIDs(g *gsOutput) []string {
	decl := map[string]gsDecl{}
	for _, d := range g.Declarations {
		decl[d.ID] = d
	}
	var out []string
	for _, p := range g.Pairs {
		a, b := decl[p.A], decl[p.B]
		x := a.File + ":" + a.Name
		y := b.File + ":" + b.Name
		if x > y {
			x, y = y, x
		}
		out = append(out, x+"|"+y)
	}
	sort.Strings(out)
	return out
}

func setOf(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func main() {
	pairsFile := flag.String("pairs", "", "gosimilarity JSON output to diff against the baseline (use - for stdin)")
	baseFile := flag.String("baseline", defaultBaseline, "baseline JSON path")
	writeBase := flag.Bool("write-baseline", false, "record the current state as the baseline and exit")
	window := flag.Int("window", 8, "normalized-line window size")
	flag.Parse()

	windows, err := scanWindows(".", *window)
	if err != nil {
		fmt.Fprintln(os.Stderr, "simcheck:", err)
		os.Exit(1)
	}
	var pairs []string
	if *pairsFile != "" {
		var raw []byte
		var err error
		if *pairsFile == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(*pairsFile)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "simcheck:", err)
			os.Exit(1)
		}
		var g gsOutput
		if err := json.Unmarshal(raw, &g); err != nil {
			fmt.Fprintln(os.Stderr, "simcheck:", err)
			os.Exit(1)
		}
		pairs = pairIDs(&g)
	}

	if *writeBase {
		out, _ := json.MarshalIndent(baseline{Pairs: pairs, Windows: windows}, "", "  ")
		if err := os.WriteFile(*baseFile, append(out, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "simcheck:", err)
			os.Exit(1)
		}
		fmt.Printf("simcheck: baseline written (%d pairs, %d windows)\n", len(pairs), len(windows))
		return
	}

	raw, err := os.ReadFile(*baseFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "simcheck: read baseline:", err)
		os.Exit(1)
	}
	var b baseline
	if err := json.Unmarshal(raw, &b); err != nil {
		fmt.Fprintln(os.Stderr, "simcheck:", err)
		os.Exit(1)
	}
	known := setOf(append(append([]string{}, b.Pairs...), b.Windows...))
	var news []string
	for _, x := range append(append([]string{}, pairs...), windows...) {
		if !known[x] {
			news = append(news, x)
		}
	}
	if len(news) > 0 {
		fmt.Fprintf(os.Stderr, "simcheck: %d new duplicate(s) vs the baseline:\n", len(news))
		for _, x := range news {
			fmt.Fprintln(os.Stderr, "  ", x)
		}
		os.Exit(1)
	}
	fmt.Printf("simcheck: no new duplicates (%d pairs, %d windows checked)\n", len(pairs), len(windows))
}
