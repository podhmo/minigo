# `staticOpTyp` / `callOpTyp` / `opTyp` の利用側: 仕分けと固定ケース

[../TODO.md](../TODO.md) の M4-9a から参照する。[fact-sites.md](./fact-sites.md) の続編として、コンパイル時の静的型情報(`opTyp`)の**利用側**を列挙し、C3′ の定義(ヒント=捨ててよい、事実=答えるか Deferred か TRAP)で仕分けた。行番号はスタック #1113 時点。

## 集約経路(問い合わせの起点)

すべての利用側は `staticOpTyp` (compile/typehint.go:213) を通る。配下の分解:

- `identOpTyp` (typehint.go:426) — ローカル束縛・パッケージ var/const。`fs` の declTyp/deferTyp を読む。
- `selOpTyp` (typehint.go:546) — `pkg.Name`。Found→Globals/`srcMemberOpTyp`(gate.go:390)、それ以外→`deferred`。
- `callOpTyp` (typehint.go:834) — 変換・組込・宣言済み関数・メンバー関数の結果型。非 Found→`deferred`。
- 補助: `valueSpecMemberOpTyp`(582), `scopeOperandExpr`(635), `nthResultOpTyp`(1009), `funcTypeResultOpTyp`(949)。

## 利用側の仕分け

| 箇所 | 関数 | 何を決めているか | 分類 | 固定ケース |
|---|---|---|---|---|
| compile/gate.go:37 | `opGate` | `==`/`!=`/case 節の mismatched-types 拒否・OpTypGate・nil 判定 | **事実**(診断+意味論) | `gate_*`, `opgate_*` 一式 |
| compile/gate.go:209 | `emitShift` | `ShiftTypedLeft` フラグ — 型なし左辺のデフォルト型 | **事実**(意味論) | `shift_const_count`, `shift_lenarr` |
| compile/expr.go:91-106 | `nonConstShiftOperand` | 非定数シフトのどちら側が非定数か | **事実** | `shift_*` |
| compile/expr.go:48-51 | `logicalNamedTyp` | `v && true` が `type B bool` の B になるか | **事実**(推論) | — |
| compile/stmt.go:459 | `rangeVarTyps` | `for k,v := range e` の束縛型 | **事実**(推論) — **deferred を落とす**(下記 G1) | `gate_rangevar`, `gate_rangedeferred` |
| compile/stmt.go:772 | `bindRecv` | `case v := <-ch` の要素型 | **事実** — **deferred + bound 経路を落とす**(G2/G3) | `gate_selectbind`, `gate_recvdeferred`(PENDING) |
| compile/assign.go:466, stmt.go:109 | `nthResultOpTyp` 束縛 | `a, b := f()` の各名の型 | **事実**(推論) — i>0 は deferred を落とす(G4) | `gate_binddeferred`, `gate_multibind` |
| compile/assign.go:497/521 | `stampInferredTyps`/`stampVarTyps` | `:=`/`var x = e` のセル宣言型(OpCoerce/OpCoerceGlobal) | **事実**(変換/強制) | `assign_inferred_tag` |
| compile/call.go:316-319 | ホイスト一時変数の `noteDeclTyp`/`noteDeferTyp` | 退避後のゲート・型 | **事実** | `convarg_*` |
| compile/call.go:654 | `unsafe.Offsetof` ベース typedef | 宣言レイアウト — ベースを評価しない | **事実** | `unsafe_sizeof_*` |
| compile/call.go:703 | `unsafe.Sizeof`/`Alignof` オペランド typedef | 結果と非評価の両方 | **事実** | `unsafe_sizeof_*`, `sizeof_*` |
| compile/call.go:906 `argStatics` → `staticTyp` → `OpLocalTyp`/`OpGlobalTyp`/`OpUpvalTyp` | ジェネリック推論に渡す引数の宣言型 | **事実**(推論) — T の束縛が結果を変える(`Describe(v)` は T=Shape) | `infer_staticbind`, `generic_sliceelem_static`, `geninfer_*` |
| compile/typehint.go:1909 `staticTyp` | assert 診断の被検査型("main.I is main.T") | **事実**(診断文言) | `assert_exprstatic`, `shadowtype_assert` |
| assign.go:312, expr.go:566, lit.go:75 `OpLocalType` | ジェネリック宣言 typedef の型引数再適用 | **事実**(意味論) | `generic_*` |

## M4-9a で見つかったギャップ(バグとして TODO に記録)

- **G1 `rangeVarTyps`**: `t.te`/`t.dte` のみ読み、`t.deferred` を捨てる。strict/DeferFacts では `for i := range p.Arr` の `i` が型なしになり、`i == d` が trap せず `false` を印字(通常は trap する)。`gate_rangedeferred` で固定。
- **G2 `bindRecv`**: 同上。`case v := <-p.Ch` が strict で要素型を失う。
- **G3 bound パッケージの Globals 経路**: `var Ch = make(chan int, 1)` のような**無名**メンバー型は `*runtime.Named` でも `hostScalarTypeDefOf` でも拾えず、**通常モードでも** elem が nil になる(セルの `Typ` には typedef があるが、`ast.Expr` への綴りが無い)。`gate_recvdeferred` を PENDING で固定。M4-9b の chunk 格納で解消候補。
- **G4 `nthResultOpTyp` 束縛**: i>0 の名は deferred を拾わない(`a, b := p.F()` で `a` のみ `noteDeferTyp`)。`b` に call 全体の deferred を付けると第一結果の型で誤答する危険があるため、現状は無言のまま — chunk 側で結果インデックスつきの事実が必要。

## 推論 vs 事実の補足

- `OpLocalTyp`/`OpGlobalTyp`/`OpUpvalTyp` 系(statics)はコンパイル時の問い合わせを持たない — セルの `Typ` を実行時に読む**実行時事実**であり、Deferred 設計とは独立して既に遅延解決している。
- `staticOpTyp` 系のコンパイル時問い合わせはすべて `selOpTyp`→`askIndexed` に集約されるため、M4-9b の「chunk への格納」は、`opTyp` の `deferred`/`dexpr` 系統(および `maybeUntyped`)をゲート以外の利用側(G1/G2/G4)へ伝搬させるのが主な作業になる。

## M4-9b の残り評価 (bind-site + staticTyp)

- `stampVarTyps`/`stampInferredTyps`(OpCoerce/OpCoerceGlobal)と `OpLocalTyp`/`OpUpvalTyp`/`OpGlobalTyp`(statics)はコンパイル時の問い合わせではなく**実行時の宣言型スタンプ**を読む経路で、Deferred 設計と独立して既に遅延解決している。chunk 事実への置き換えは `t.te` の綴りをジェネリック・他ファイルのスコープで再解決する必要があり、スタンプ済みセルより正確にならないため置き換えない。
- 代わりに `staticTyp`(assert 診断 + `argStatics` 推論)の deferred 穴だけを塞いだ:`declDefer` を持つ束縛名は `OpDeferredValueTyp` でマーカー式を実行時解決する(range/recv/複数結果束縛の合成 IndexExpr/IndexListExpr)。
