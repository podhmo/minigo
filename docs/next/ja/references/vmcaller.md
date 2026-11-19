# VMCaller の分割

[../TODO.md](../TODO.md) の P2-1〜P2-4 から参照する。

## 計測（main `a3f4e24`、テスト以外の `.go`、examples/testdata/tools/.claude を除く）

- `VMCaller` を引数に取る関数と関数リテラルは 360 個。
  - 96 個は一切使っていない（P2-3 の対象）。
  - 184 個は helper に受け渡すだけ（P2-2 の対象）。
  - 80 個がメソッドを直接呼んでいる。

| メソッド | 呼ぶ関数の数 | 主なファイル |
|---|---:|---|
| `Call` | 24 | intrinsics.go、minigo.go、minireflect/*、runtime/value.go、vm/vm.go |
| `TypeOf` | 16 | builtins.go、embed.go、intrinsics.go、runtime/value.go |
| `Convert` | 13 | builtins.go、embed.go、intrinsics.go、iterseq.go、minireflect |
| `ElemZero` | 12 | builtins.go、embed.go、intrinsics.go |
| `Zero` | 10 | builtins.go、embed.go、intrinsics.go、minireflect |
| `ElemOf` | 6 | intrinsics.go、iterseq.go |
| `Copy` | 5 | builtins.go、embed.go |
| `ResolveType`、`Equal` | 4 | intrinsics.go |
| `NewMap`、`Member` | 3 | |
| `Package`、`CallerFrame`、`MapKeyOperand`、`ArrayLenOf`、`CallerPCs` | 2 | |
| `Recover`、`FieldTypes`、`Spawn`、`MethodSetOf` | 1 | |
| `Task` | 0（テストのみ） | |

## 隠れた capability

21 メソッドとは別に、型アサーションで次のメソッドが使われている。

| メソッド | 場所 |
|---|---|
| `ElemZero`（3 箇所） | builtins.go:202, 414, 581 |
| `HostClear` | builtins.go:384 付近 |
| `HostMapDelete` | builtins.go:593 付近 |
| `MaterializeConstErr` | intrinsics.go:10652 |
| `HostNilTwin` | minigo_reflect.go:41 |
| `CallCatching` | vm/vm.go:7442 |

`vm.vmOf`（vm/vm.go:4994）は `*VM`、`ownerCaller`、`processCaller` にダウンキャストしており、7 箇所から呼ばれている。

## 絞り込みの境界（P2-2b）

観測専用（型アサーションして状態を読むだけ、呼び出し能力を持たない）の引数は `runtime.CallerRef`（= `any`）にする。`sched.go` の park/wake API、`vmOf`/`procOf`/`procDoneOf`、`watchCtxDone`/`newChanFeed` 系がこのパターン。

host proxy の callback は member 操作だけが多いので、`callbackContext`（`VMCaller`→`VMCaller`）に対して member-only 版の `callbackMemberContext`（`MemberCaller`→`MemberCaller`）を用意した。`toReflectValue`/`CallCatching`/`c.Fn` に渡す側は `VMCaller` のまま残る — `bindCaller`/`callbackContext` 自身をこれ以上絞ると「VM が要る側に型を合わせてから受け渡す」二重の型付けになるだけで情報が増えない。

## よく受け渡されている helper（P2-2 の対象の候補）

`runtime.IfaceMember`（18）、`goJSONSeen`（10）、`callOrPanic`（10）、`scriptMapSeqArg`（9）、`runePred`（8）、`jsonShape`（8）、`hostErrOf`（16）、`display`（8）、`jsonCallMethod`（6）、`h.asReaderVM`（6）、`callBool`（6）、`scriptSliceArg`（5）。

## 再計測用のスキャナ

`go/ast` だけを使う。`go run . <repo root>` で実行する。

```go
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func isVMC(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		return t.Sel.Name == "VMCaller"
	case *ast.Ident:
		return t.Name == "VMCaller"
	}
	return false
}

func main() {
	root := os.Args[1]
	methodCount := map[string]int{}
	methodFiles := map[string]map[string]bool{}
	sets := map[string]int{}
	total, none, passOnly := 0, 0, 0
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() && (strings.Contains(path, "/examples") || strings.Contains(path, "/testdata") || strings.Contains(path, "/.claude") || strings.Contains(path, "/tools")) {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		var visit func(ft *ast.FuncType, body *ast.BlockStmt)
		visit = func(ft *ast.FuncType, body *ast.BlockStmt) {
			if body == nil || ft.Params == nil {
				return
			}
			var names []string
			for _, fld := range ft.Params.List {
				if isVMC(fld.Type) {
					for _, n := range fld.Names {
						if n.Name != "_" {
							names = append(names, n.Name)
						}
					}
				}
			}
			if len(names) == 0 {
				return
			}
			total++
			used := map[string]bool{}
			passed := false
			ast.Inspect(body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if se, ok := x.Fun.(*ast.SelectorExpr); ok {
						if id, ok := se.X.(*ast.Ident); ok {
							for _, nm := range names {
								if id.Name == nm {
									used[se.Sel.Name] = true
								}
							}
						}
					}
					for _, a := range x.Args {
						if id, ok := a.(*ast.Ident); ok {
							for _, nm := range names {
								if id.Name == nm {
									passed = true
								}
							}
						}
					}
				}
				return true
			})
			var ks []string
			for k := range used {
				ks = append(ks, k)
				methodCount[k]++
				if methodFiles[k] == nil {
					methodFiles[k] = map[string]bool{}
				}
				methodFiles[k][rel] = true
			}
			sort.Strings(ks)
			if len(ks) == 0 {
				if passed {
					passOnly++
				} else {
					none++
				}
			}
			key := strings.Join(ks, ",")
			if passed {
				key += " +passes"
			}
			sets[key]++
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				visit(x.Type, x.Body)
			case *ast.FuncLit:
				visit(x.Type, x.Body)
			}
			return true
		})
		return nil
	})
	fmt.Printf("funcs taking VMCaller: %d; use no method: %d (of which only pass it on: %d)\n", total, none+passOnly, passOnly)
	var ms []string
	for k := range methodCount {
		ms = append(ms, k)
	}
	sort.Slice(ms, func(i, j int) bool { return methodCount[ms[i]] > methodCount[ms[j]] })
	for _, m := range ms {
		var fs []string
		for f := range methodFiles[m] {
			fs = append(fs, f)
		}
		sort.Strings(fs)
		fmt.Printf("  %-14s %4d  %s\n", m, methodCount[m], strings.Join(fs, " "))
	}
	type kv struct {
		k string
		v int
	}
	var kvs []kv
	for k, v := range sets {
		kvs = append(kvs, kv{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
	fmt.Println("top method sets:")
	for i, x := range kvs {
		if i > 15 {
			break
		}
		fmt.Printf("  %4d  {%s}\n", x.v, x.k)
	}
}
```

## 再分類（P2-2/P2-3 の確定、スタック先端の HEAD）

識別子参照を数えるスキャナで全 named `VMCaller` 引数を3分類した。

- **未使用（参照0回）**: 0 件。初回計測の 96 件は P2-1〜P2-2b の CallerRef 化・削除で解消済み。
- **pass-only（call の引数で受け渡すだけ）**: 156 件。ほぼ全件が funclit で、シグネチャが `BuiltinFunc.Fn`（`func(runtime.VMCaller, []runtime.Value)`）、iterseq の yield、hostbridge の登録型で固定されている。引数を `MemberCaller` 等に絞ると渡し先の helper のシグネチャまで連鎖して下げる必要があり、「VM が要る側に型を合わせてから受け渡す」二重の型付けを増やすだけで情報量が増えないため現行維持。
- **ref-only（参照はあるがメソッド呼び出しなし）**: 11 件。capability の型アサーション（`DeepEqual` の `HostNilTwiner`、`materializerOf`）と、後で必要になるための保存（`RValue.vc`、`Panic.VC`、`bindCaller`/`callbackContext`）の2種。`materializerOf` の引数は `runtime.CallerRef` に絞った（観測のみで呼び出し能力を使わない、P2-2b と同じパターン）。BuiltinFunc.Fn のシグネットで固定される funclit と、VM 経由を維持する保存系は `VMCaller` のまま。

完了条件: 未使用 0 件を維持し、pass-only はシグネチャ固定の funclit であることを文書で固定した。

