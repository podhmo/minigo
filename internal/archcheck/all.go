package archcheck

import "fmt"

// CheckAll runs every repository-level check the archcheck CLI and the
// real-repo tests share (M2-2): the import graph, the shared-field
// boundaries of sharedAssets and PackageImage, the file-size budgets,
// the banned selector calls and the banned imports.
func CheckAll(root string) ([]Violation, error) {
	var out []Violation
	g, err := Build(root)
	if err != nil {
		return nil, err
	}
	out = append(out, Check(g, Rules)...)
	for _, target := range [][2]string{{".", "sharedAssets"}, {"runtime", "PackageImage"}} {
		vs, err := CheckSharedFields(root, target[0], target[1], SharedStateTypes, SharedFuncExempt)
		if err != nil {
			return nil, err
		}
		out = append(out, vs...)
	}
	sizes, err := Sizes(root)
	if err != nil {
		return nil, err
	}
	out = append(out, CheckSizes(sizes, FileBudgets)...)
	bv, err := CheckBannedCalls(root, BannedCalls)
	if err != nil {
		return nil, err
	}
	out = append(out, bv...)
	out = append(out, CheckBannedImports(root, BannedImports)...)
	return out, nil
}

// CheckBannedCalls scans each package dir in table for the selector
// calls it must never make — the check shape the CLI and tests share
// so their coverage cannot diverge.
func CheckBannedCalls(root string, table map[string][]string) ([]Violation, error) {
	var out []Violation
	for dir, names := range table {
		banned := map[string]bool{}
		for _, n := range names {
			banned[n] = true
		}
		reps, err := BannedSelectors(root, []string{dir}, banned)
		if err != nil {
			return nil, err
		}
		for _, r := range reps {
			out = append(out, Violation{dir, "", fmt.Sprintf("%s:%d calls .%s, banned in %s/", rel(root, r.File), r.Line, r.Name, dir)})
		}
	}
	return out, nil
}
