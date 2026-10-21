package main

import (
	"fmt"
	"io/fs"
	"os"
)

// The untyped left operand of a non-constant shift takes the type the
// context gives it — "the type it would assume if the shift expression
// were replaced by its left operand alone". In `m & (1 << n)` that is
// m's type, so io/fs's FileMode.String (`m&(1<<uint(32-1-i))`) must not
// trip a "mismatched types FileMode and int" reject.

type M uint32

type Small int8

func main() {
	s := uint(3)
	var m M = 0xff
	fmt.Println(m&(1<<s), m&(1<<uint(s)) != 0, m|1<<(s+8)) // 8 true 2303
	fmt.Printf("%T %T\n", m&(1<<s), 1<<s+m)                // main.M main.M

	var sm Small = 1
	fmt.Println(sm<<s, sm+1<<(s+4), 1<<(s+4)+sm) // 8 -127 -127

	var u8 uint8 = 7
	fmt.Println(u8+1<<s, u8&^(1<<(s-1)), 1<<(s+5)+u8) // 15 3 7
	var i64 int64 = 5
	fmt.Println(i64<<s, 1<<(s+60) > i64, i64 == 1<<s-3) // 40 false true
	fmt.Println(1<<s == m, m != 1<<(s+30))              // false true

	// io/fs FileMode printing goes through the same shift
	fmt.Println(fs.ModeDir)                              // d---------
	fmt.Println(fs.ModePerm | fs.ModeDir)                // drwxrwxrwx
	fmt.Println(fs.FileMode(0644), fs.ModeSymlink|0o777) // -rw-r--r-- Lrwxrwxrwx
	fmt.Println(fs.ModeDir|0o755, fs.FileMode(0o700))    // drwxr-xr-x -rwx------
	fmt.Println(os.ModeDir|0o755, os.ModePerm)           // drwxr-xr-x -rwxrwxrwx
	var fm fs.FileMode = 0o600
	fmt.Println(fm, fm.IsDir(), fm.Perm(), fm|fs.ModeDir) // -rw------- false -rw------- drw-------
}
