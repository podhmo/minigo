package dep2

import dep3 "github.com/podhmo/minigo/testdata/difffuzz/nilcmp_indirect/dep3"

// Get's result type is dep3's List — main never imports dep3 itself.
func Get() dep3.List { return nil }
