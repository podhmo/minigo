package main

import (
	"fmt"
)

var (
	v_s_0  string         = ""
	v_s_1  string         = "a,b,,c"
	v_s_2  string         = "  Hello, World  "
	v_s_3  string         = "héllo wörld"
	v_s_4  string         = "日本語テキスト"
	v_s_5  string         = "key=value; k2=v2"
	v_s_6  string         = "42"
	v_s_7  string         = "-3.5e2"
	v_s_8  string         = "line1\nline2\n"
	v_s_9  string         = "\t tab\tsep "
	v_s_10 string         = "GoGoGo"
	v_s_11 string         = "true"
	v_n_0  int            = 0
	v_n_1  int            = 1
	v_n_2  int            = 2
	v_n_3  int            = 3
	v_n_4  int            = -1
	v_n_5  int            = 5
	v_n_6  int            = 10
	v_b_0  bool           = true
	v_b_1  bool           = false
	v_r_0  rune           = 'a'
	v_r_1  rune           = 'Z'
	v_r_2  rune           = 'é'
	v_r_3  rune           = '日'
	v_r_4  rune           = ' '
	v_r_5  rune           = ','
	v_r_6  rune           = '7'
	v_r_7  rune           = '\n'
	v_c_0  byte           = 'a'
	v_c_1  byte           = '0'
	v_c_2  byte           = ' '
	v_c_3  byte           = 0xff
	v_f_0  float64        = 0.0
	v_f_1  float64        = 1.5
	v_f_2  float64        = -2.25
	v_f_3  float64        = 100.0
	v_f_4  float64        = 0.1
	v_f_5  float64        = 1e6
	v_ss_0 []string       = nil
	v_ss_1 []string       = []string{}
	v_ss_2 []string       = []string{"b", "a", "c"}
	v_ss_3 []string       = []string{"x", "", "y", "x"}
	v_ss_4 []string       = []string{"Hello", "héllo", "HELLO"}
	v_bs_0 []byte         = nil
	v_bs_1 []byte         = []byte("abc")
	v_bs_2 []byte         = []byte("héllo")
	v_bs_3 []byte         = []byte{0, 'A', 0xff}
	v_rs_0 []rune         = nil
	v_rs_1 []rune         = []rune("héllo")
	v_rs_2 []rune         = []rune{'日', '本'}
	v_m_0  map[string]int = nil
	v_m_1  map[string]int = map[string]int{}
	v_m_2  map[string]int = map[string]int{"a": 1, "b": 2, "c": 3}
	v_m_3  map[string]int = map[string]int{"x": -1, "": 0}
)

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	v := f()
	fmt.Printf("%d: %T %v\n", i, v, v)
}

func id[T any](x T) T { return x }

func main() {
	try(0, func() any { return v_m_0 })
}
