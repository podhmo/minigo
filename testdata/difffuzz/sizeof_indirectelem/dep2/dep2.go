package dep2

import dep3 "github.com/podhmo/minigo/testdata/difffuzz/sizeof_indirectelem/dep3"

func Get() dep3.List { return dep3.List{1, 2, 3} }
