package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/ptrconv_crosspkg_field/inner"
)

// `type Tag inner.Tag` converted back with (*inner.Tag)(t): inner's
// methods read their unexported fields through the converted pointer —
// golang.org/x/text/language's Tag.isCompact.

type Tag inner.Tag

func (t *Tag) isCompact() bool { return (*inner.Tag)(t).IsCompact() }

func (t *Tag) lang() int { return (*inner.Tag)(t).Lang() }

func main() {
	t := Tag(inner.Make(3))
	fmt.Println(t.isCompact(), t.lang())
}
