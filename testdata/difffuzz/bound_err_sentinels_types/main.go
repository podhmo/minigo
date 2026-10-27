package main

// Bound packages expose their error sentinels (as the host objects) and
// error types, so comparisons, errors.As and type asserts on host-made
// errors work: path/filepath.ErrBadPattern, bufio's scanner sentinels and
// split funcs, *exec.Error / *exec.ExitError, *time.ParseError and
// *json.SyntaxError.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	_, err := path.Match("[", "x")
	fmt.Println(err, err == path.ErrBadPattern)
	_, err = filepath.Match("[", "x")
	fmt.Println(err, errors.Is(err, filepath.ErrBadPattern), filepath.ErrBadPattern == path.ErrBadPattern)

	sc := bufio.NewScanner(strings.NewReader("a b STOP c d"))
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		adv, tok, err := bufio.ScanWords(data, atEOF)
		if string(tok) == "STOP" {
			return adv, tok, bufio.ErrFinalToken
		}
		return adv, tok, err
	})
	for sc.Scan() {
		fmt.Println(sc.Text())
	}
	fmt.Println(sc.Err())
	sc = bufio.NewScanner(strings.NewReader("x y\nz"))
	sc.Split(bufio.ScanWords)
	fmt.Printf("%T\n", bufio.ScanWords)
	var sf bufio.SplitFunc = bufio.ScanLines
	fmt.Println(sf == nil)
	adv, tok, _ := sf([]byte("ab\ncd"), false)
	fmt.Println(adv, string(tok))
	n := 0
	for sc.Scan() {
		n++
	}
	fmt.Println(n)
	sc = bufio.NewScanner(strings.NewReader(strings.Repeat("x", 100)))
	sc.Buffer(make([]byte, 10), 10)
	for sc.Scan() {
	}
	fmt.Println(sc.Err() == bufio.ErrTooLong)
	sc = bufio.NewScanner(strings.NewReader("abc"))
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) { return -1, nil, nil })
	for sc.Scan() {
	}
	fmt.Println(sc.Err() == bufio.ErrNegativeAdvance)
	sc = bufio.NewScanner(strings.NewReader("abc"))
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) { return 100, nil, nil })
	for sc.Scan() {
	}
	fmt.Println(sc.Err() == bufio.ErrAdvanceTooFar)

	_, err = exec.LookPath("no-such-cmd-zz-minigo")
	var ee *exec.Error
	fmt.Println(errors.Is(err, exec.ErrNotFound), errors.As(err, &ee), ee.Name)
	err = exec.Command("sh", "-c", "exit 3").Run()
	var xe *exec.ExitError
	fmt.Println(errors.As(err, &xe), xe.ExitCode())

	_, err = time.Parse("2006", "x")
	var pe *time.ParseError
	fmt.Println(errors.As(err, &pe), pe.Layout, pe.Value)

	var t any
	err = json.Unmarshal([]byte(`{`), &t)
	se, ok := err.(*json.SyntaxError)
	fmt.Println(ok, se.Offset)
	var se2 *json.SyntaxError
	fmt.Printf("%v %T\n", errors.As(err, &se2), se2)
}
