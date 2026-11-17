# スロット分類

[../TODO.md](../TODO.md) の P4-2 から参照する。

## 計測（root スイートと examples 6 つ、重複除去後 4,161 chunk、生の数で 11,402）

| | 割合 |
|---|---:|
| 現状 `chunkCellsEscape` が true | 44.5%（ローカルを持つ chunk に限れば 56%） |
| 正確な規則（`&`、クロージャによるキャプチャ、特殊形式） | 9.3% |
| ＋実行時に観測されたポインタレシーバの束縛（現実的な値） | 9.7%（Boxed のローカル 5.0%） |
| ＋`x.M()` のレシーバをすべて escape とみなす（上限） | 27.9%（Boxed 15.1%） |

## `OpLocalRef` の理由（11.6k 命令）

| 理由 | 割合 | 生成箇所 | escape するか |
|---|---:|---|---|
| selector の読み出し `x.f`、メソッド値 `x.M` | 35.9% | `selectorBase`（compile.go:6657）、`expr`（:5212） | しない。ただしポインタレシーバのメソッド値は除く |
| 呼び出しのレシーバ `x.M()` | 34.7% | `calleeExpr`（:6693） | ポインタレシーバのときだけ（実行時に観測されたのは 31 chunk） |
| `x = v` | 10.0% | `refTarget`（:4043）、`assign`（:1730） | しない |
| `x.f = v` | 6.9% | `refTargetBase` | しない |
| `x[i] = v`、多重代入、`op=` | 各 3.0% | :1705 | しない |
| `&x` | 1.9% | `unary` → `refRef`（:644/:5365） | する |
| `*p = v`、`x.f++`、`x[i]++` | 1.3% | :1519 | しない |
| `&x.f`、`&x[i]` | 0.3% | :5370/:5373 | する |

主因は `selectorBaseIsVar`（compile.go:4140 付近）が任意の変数に対して true になることである。

## escape しなくても節約が小さい理由

`OpNewLocal`（vm.go:2597）は `:=`/`var` のたびに新しい `Cell` を確保し、`OpRenewVar`（vm.go:2607）はループの反復ごとに確保する。そのため #1024 のブロック再利用（`prepFrame`、vm.go:1856 付近）が効くのは引数だけである。

## 移植が必要な箇所

| 箇所 | 内容 |
|---|---|
| vm.go:935 `chunkCellsEscape`、vm.go:1856 付近 `prepFrame` | cell ブロックと `locals []*Cell` |
| vm.go:2597/2607 | `OpNewLocal`/`OpRenewVar` |
| vm.go:3088 `OpMakeClosure` | `f.locals[d.Index]` をキャプチャする（Boxed なら問題ない） |
| vm.go:3640 `selectMember`、vm.go:7552 `structMember`/`namedMember` | ポインタレシーバでは cell をレシーバにし、値レシーバでは cell からコピーする |
| vm.go:1156 `assignRef` | `OpSetRefs` を経由する ref ストア |
| vm.go:17576 `specialCtx.Eval` | 呼び出し元の cell を共有する。`trySpecial`（compile.go:6641）が見えるローカルをすべてスナップショットする |
| vm.go:17409 `specialCtx.Resolve` | `f.locals[slot].Elem` を読む |
| vm.go:8886 `localTypedefOf` | すべてのローカルと upvalue の cell を走査する |
| vm.go:2182/3356 | 名前付き戻り値 |
| vm.go:3218 | range のイテレータのスロット |

いずれもブロッカーではない。`OpEvalAST`、`goto`、`defer`/`recover`、`inspect` は影響を受けない。行番号は main `a3f4e24` のもので、P1 のファイル分割の後は変わる。
