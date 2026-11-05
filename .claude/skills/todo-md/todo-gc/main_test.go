package main

import (
	"strings"
	"testing"
)

const header = "# TODO archive\n\n> note\n"

func lines(s ...string) string { return strings.Join(s, "\n") + "\n" }

func TestGC(t *testing.T) {
	cases := []struct {
		name        string
		todo        string
		archive     string
		wantTODO    string
		wantArchive string
		wantWarn    int
	}{
		{
			name: "subtree is split into one line per item",
			todo: lines(
				"# TODO",
				"",
				"- [x] **A**: one",
				"  two.",
				"  - [x] **B**: b",
				"  - [x] **C**: c",
				"    - [x] **D**: d",
				"  - 完了の条件: done",
				"- [ ] **E**: e",
			),
			archive:  header,
			wantTODO: lines("# TODO", "", "- [ ] **E**: e"),
			wantArchive: header + lines(
				"",
				"- [x] **A**: one two.",
				"- [x] A > **B**: b",
				"- [x] A > **C**: c",
				"- [x] A > C > **D**: d",
				"- A > 完了の条件: done",
			),
		},
		{
			name: "done children leave an open parent in place",
			todo: lines(
				"- [-] **P**: p",
				"  - [x] MVP: first",
				"  - [ ] later",
				"  - 完了の条件: stays",
			),
			archive: header,
			wantTODO: lines(
				"- [-] **P**: p",
				"  - [ ] later",
				"  - 完了の条件: stays",
			),
			wantArchive: header + lines("", "- [x] P > MVP: first"),
		},
		{
			name:        "done item with an open child is kept and reported",
			todo:        lines("- [x] **P**: p", "  - [ ] open"),
			archive:     header,
			wantTODO:    lines("- [x] **P**: p", "  - [ ] open"),
			wantArchive: header,
			wantWarn:    1,
		},
		{
			name:        "blank lines left behind are collapsed",
			todo:        lines("## To Be Implemented", "", "- [x] **A**: a", "", "- [ ] **B**: b"),
			archive:     header,
			wantTODO:    lines("## To Be Implemented", "", "- [ ] **B**: b"),
			wantArchive: header + lines("", "- [x] **A**: a"),
		},
		{
			name: "old archive entries are normalized",
			todo: lines("- [ ] **E**: e"),
			archive: header + lines(
				"",
				"- [x] **A**: wrapped",
				"  text",
				"  - [x] **B**: 日本語の",
				"    文、",
				"    URL と `code`",
				"> parent: P › Q",
				"- [x] **R**: r",
				"",
				"> parent: S",
				"  - [x] indented: kept its old indentation",
				"    - [x] deeper",
			),
			wantTODO: lines("- [ ] **E**: e"),
			wantArchive: header + lines(
				"",
				"- [x] **A**: wrapped text",
				"- [x] A > **B**: 日本語の文、URL と `code`",
				"- [x] P > Q > **R**: r",
				"",
				"- [x] S > indented: kept its old indentation",
				"- [x] S > indented > deeper",
			),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := GC(c.todo, c.archive)
			if got.TODO != c.wantTODO {
				t.Errorf("TODO:\n%s\nwant:\n%s", got.TODO, c.wantTODO)
			}
			if got.Archive != c.wantArchive {
				t.Errorf("archive:\n%s\nwant:\n%s", got.Archive, c.wantArchive)
			}
			if len(got.Warnings) != c.wantWarn {
				t.Errorf("warnings = %q, want %d", got.Warnings, c.wantWarn)
			}
			again := GC(got.TODO, got.Archive)
			if again.TODO != got.TODO || again.Archive != got.Archive || again.Moved != 0 {
				t.Errorf("second run changed the files:\n%s", again.Archive)
			}
		})
	}
}

func TestTitle(t *testing.T) {
	for in, want := range map[string]string{
		"**Bold** (link): text": "Bold",
		"MVP: rest":             "MVP",
		"MVP：全角":                "MVP",
		"plain text":            "plain text",
	} {
		if got := title(in); got != want {
			t.Errorf("title(%q) = %q, want %q", in, got, want)
		}
	}
}
