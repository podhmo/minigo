package main

// regexp.Regexp names the host type the regexp constructors return: a
// non-nil *regexp.Regexp in an interface asserts and type-switches to
// it (plain or renamed import), and *regexp.Regexp slots, fields and
// results hold one.

import (
	"fmt"
	"regexp"
	rx "regexp"
)

type matcher struct {
	re *regexp.Regexp
}

func (m matcher) match(s string) bool { return m.re.MatchString(s) }

func build(s string) *rx.Regexp { return rx.MustCompile(s) }

func main() {
	var c any = regexp.MustCompile("a")
	q, ok := c.(*regexp.Regexp)
	fmt.Println(ok, q.MatchString("cat"))
	q2, ok := c.(*rx.Regexp)
	fmt.Println(ok, q2.String())
	switch v := c.(type) {
	case *regexp.Regexp:
		fmt.Println("regexp", v.String())
	default:
		fmt.Println("other")
	}
	_, ok = c.(fmt.Stringer)
	fmt.Println(ok)
	fmt.Printf("%T\n", c)

	var rr *regexp.Regexp = regexp.MustCompile("b+")
	fmt.Println(rr.FindString("abbbc"))
	m := matcher{re: build("^x")}
	fmt.Println(m.match("xyz"), m.match("yz"))
	res := []*regexp.Regexp{regexp.MustCompile("1"), build("2")}
	for _, r := range res {
		fmt.Println(r.MatchString("12"))
	}
	var none *regexp.Regexp
	fmt.Println(none == nil)
}
