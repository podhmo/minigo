# 言語機能の網羅的ファジング実験 — バグ発見と修正のレポート

対象: `podhmo/minigo` の `main`（PR #27 の並行処理実装がマージ済みの状態）
方法: 並行処理回（`fuzz-concurrency.md`）と同じく、仕様上あり得る書き方を「思考を頼りに」系統立てて投入する手作り PBT/fuzzing。今回は**言語機能の表面**を対象にする（並行処理は前回済み）。テーマを列挙 → 各テーマに境界ケースを書く → minigo と本家 Go の出力を diff → 差分を潰す、のループ。

ブランチ: `devin/1790895111-lang-fuzz`（base は `main`）

---

## 1. ハーネス

`~/langfuzz/` に差分テストハーネスを構築した:

- `cases/<theme>/main.go` — 1 ディレクトリ = 1 テーマのプログラム（`func main()`）
- `run.sh` — 各ケースについて `cd cases/X && go run .` と `./minigo ./cases/X` の stdout+stderr を diff
- 判定: `PASS`（出力一致）/ `DIFF` / `TRAP`（minigo が runtime trap で終了）/ `PASS-REJECT`（Go がコンパイルエラー、minigo は trap=最も近い「大きく失敗」）/ `ACCEPT`（Go が拒否するのに動いてしまった＝最悪の判定）

ケース群（38 本 + neg 4 本 + lim 3 本）: `appendalias` `arith` `arrays` `assign` `builtins` `consts` `control` `convs` `defers` `destruct` `embed` `errors2` `fmtverbs` `forrangeint` `funcs` `generics` `initfuncs` `initorder` `interfaces` `iterators` `lim-*` `loopvars` `mapkeys` `maps` `methods` `namedret` `neg-*` `nilfieldwrite` `pkgs` `ptrs` `scope` `slices` `strings` `structs` `typednilcall` `tyswitch` — 合計約 2300 行。

**最終状態: PASS=35, PASS-REJECT=4, DIFF=0, TRAP=3**（TRAP は全て意図した設計上の制限＝lim-*）。

## 2. 発見したバグと修正（今回分）

### コンパイル層

- **B1. マップリテラルの省略キーが壊れていた**: `map[K]string{{1,2}:"x"}` — 省略形のキーが `Def=nil` の untyped struct になり、canonical key 化で nil deref。`compileLit` が省略キーを `MapType.Key` でコンパイルするよう `peelLitType` を追加（配列・マップ・ポインタ・括弧を剥がす共通ヘルパ）。
- **B2. `(*p) += 10` が trap**: 複合代入のターゲット正規化が `ParenExpr` を剥がさなかった。
- **B3. パッケージ変数の初期化順が Go と違う**: `var a = f()+b; var c` は依存駆動 DFS で `[b a c]` とすべきところ、全域 Kahn で `[b c a]` になっていた。`orderSpecs` をソース順 walk + 依存の再帰 emit に書き換え。
- **B4. `x &^= y` / MinInt64 リテラル**（前段で修正済み）。

### 値の表現・同一性

- **B5. `struct{A int}{1} == struct{A int}{1}` が false**: `eqlValue` が `Def` のポインタ同一性を要求していた。匿名 struct 同士はフィールド名一致で同一型とみなす `structDefsEq` を追加。
- **B6. 配列キーの map が全部ミス**: `m[[2]int{1,2}]` が常に 0 — canonical key の型タグが匿名 typedef を `%p`（ポインタ）で表現していて、別リテラルは別キーになっていた。`typeTagOf` を構造的なスペリング（`anonTag`: `struct{A:int}` 等）に変更し、`pkg.Path.Name` で同名別パッケージも区別。
- **B7. スライスキーが受理されていた**: `map[[]int]int` — `CanonicalKey` が Slice を無条件に repr 化していた。`arrayTypedef`（`ArrayType.Len != nil`）で固定長配列だけを通し、素のスライスは `hash of unhashable type` panic（Go と同等）。
- **B8. `a[:]` が配列 typedef を引き継いでいた**: `[4]int` のスライス式が `Typ=[4]int` を持ち、`cap(a[1:3])` が 2 を返し、`b := s` で配列の値コピーが走って共有が消えた。`v.slice` が `sliceTypOf` で `[]Elem` typedef を作る。
- **B9. `-uint8(5)` が -5 を返す**: `var x uint8 = 5` は maskInt 済みだが裸 int64 として束縛され、単項マイナスでタグを失っていた。sized-int ビルトイン宣言を `Named` でタグ付けし、unary/binary の再タグで `maskInt` を適用。`type MyU8 uint8` は `sizedNameOf` で underlying 名を解決する。
- **B10. インタフェース経由の typed-nil メソッド呼び出し**: `var i I = (*NP)(nil); i.M()` が `IfaceNil` をレシーバにして死んでいた → `TypedNil` に変換してからメソッド探索。

### 変換・ジェネリクス

- **B11. `(*[N]int)(s)` / `[N]int(s)` 未対応**: `convertPointer`/`convertSlice` に slice→array(-ptr) 変換を追加（長さ不足は実行時 panic）。
- **B12. ローカル `type` がジェネリクス推論に効かない**: `Sum([]MyInt{...})`（関数内で `type MyInt int`）→ 型名解決は package index しか見ないため、`argTypedef` が要素の型証拠を伝える + `elemTypedef` を frame-aware にしてローカル const セルを走査するフォールバックを追加。

### fmt/errors 出力面

- **B13. `%T` が `*minigo.fmtValue` と表示**: host fmt は `Formatter.Format` を `%T`/`%p` では呼ばない（reflect 直読み）。format 文字列を走査して `%T`→`%s` に書き換え、対応引数を `scriptTypeString`（`[]int`、`main.K` 等のスクリプト型スペリング）に差し替える `rewriteTypeVerbs` を `ffn` に追加。
- **B14. `%q`/`%s`/`%x` on `[]byte` が数列のまま**: `sliceBytes` で全要素が byte 範囲なら文字列として処理。ついでに `%c`/`%d`/`%x` 等のスカラー動詞は複合内で要素ごとに降りる（`elemVerb`）。
- **B15. `%#v`/`%T` が `main.` プレフィックスを落としていた**: `typedefSpelling` の "main" 除外を撤廃。
- **B16. `fmt.Errorf("%w")` が `%!w(...)` + Unwrap nil**: `%w`→`%v` 書き換え + `wrapError`（msg+cause の host error）。スクリプト側 error は `scriptError`（Error() を VM に callback する host error）として包み、`errors.Is`/`Unwrap` が chain を歩けるようにした。`errors.As` も「最初の非 nil」ではなく chain 走査 + 型名一致で代入先を決める。
- **B17. `print`/`println` が stdout に書いていた**: Go の仕様通り `os.Stderr` に変更（`print` は非文字列オペランド間のみ空白）。

### メッセージ形状（パニック系）

- **B18. `panic(nil)` の recover 値**: `*runtime.PanicNilError` を返す（`%T` で Go と同じスペル）。`Error()` に `runtime error: ` プレフィックスが抜けていたのも修正。
- **B19. nil func 呼び出し** → `call of nil function` → Go 相当の `invalid memory address or nil pointer dereference`。
- **B20. nil スライスの index** → `index out of range` → `index out of range [i] with length n`。
- **B21. 失敗した型アサーション** → `string is not int` → `interface conversion: interface {} is string, not int`（静的インタフェース側は IfaceNil 時のみ正確に名前を出す近似）。

### コンパイルで拒否すべきもの（最も近い形で大きく失敗）

- **B22. `&m[k]` が動いてしまった**: `OpIndexRef` でマップ要素の参照は runtime trap（Go はコンパイルエラー — 一番近い loud failure）。

## 3. 制限事項として文書化（lim-*）

- **`lim-complex`**: `complex()`/`real()`/`imag()` 未対応（complex64/128 が値型にない）。TRAP で loud に失敗する。
- **`lim-threeidx`**: 3 引数スライス `s[a:b:c]` 未対応。TRAP。
- **`lim-bigconst`**: `1 << 100` のような int64 を超える untyped const — Go は任意精度だが minigo は int64/float64 に落とすので 0/オーバーフロー。TRAP。

## 4. 残っている近似（バグではないがズレる）

- `x.(T)` の静的インタフェース名は `interface {}` に近似される（動的値が非 nil の場合、静的型が失われているため）。`error` 等の名付きインタフェースは IfaceNil 経由で正しい名前が出る。
- `%p` on script ポインタは GoValue/Cell のアドレス表現（実アドレスではない）。
- `errors.As` の型照合は「葉の型名一致」の近似（`main.MyErr` と `pkg2.MyErr` を区別しない）。
- float32 の丸め（`var f float32` への代入時の精度落ち）は未実装。
- スクリプト生成 panic 値の一部（`panic(struct{...})` 等）が `*runtime.Panic` ではなく生値で飛ぶ — `%T` が素の型名になる。
- `go test` で拾えていない領域: `go list`/`go/types` 非使用という設計方針上、一部のコンパイル時検査（代入可能性の完全な型検査等）は実行時まで落ちる。

## 5. 回帰テスト

`testdata/fuzzfix/main.go` + `minigo_test.go` の `TestFuzzFixes`（22 項目）で、直したバグの各々に 1 関数を対応させた。`make test` / `make lint` / `make format` は全てクリーン。

## 6. 考察 — 「壊れてないか」の検証方法について

ユーザーの問い「現状はハッピーパスの確認にすぎないかも」に対する答え:

- **差分実行は強力だった**: `go run .` と `minigo` の stdout/stderr をそのまま diff するだけで、出力整形（`%v` の `{}`/`[]` 構造、map の順序、エラーメッセージの語彙）まで全て網羅される。プロパティを書かなくても「同じ出力」という最強の不変条件が使える。
- **思考ガイドの列挙が効いた**: 前回（並行処理）同様、仕様の「あり得る構文」の列挙 → 各ケースの Go 挙動を予想 → diff で検証、という手順で、機械的なランダム fuzzing では設計できないケース（`map[K]string{{1,2}:"x"}` の省略キー、`(*[3]int)(s)`、`var a = f()+b` の依存順）が見つかった。
- **「壊れてないか」の判定は多段**: (a) 明確な panic/クラッシュ、(b) 出力不一致、(c) Go は reject するのに受理（ACCEPT＝最悪）、(d) Go は panic するが minigo は trap（実行時 vs コンパイル時の違いは許容）。これらを分類しないと「動いてしまった静的エラー」が見落とされる（`neg-mapaddr` が典型）。
- **隠れバグの温床は「ヘルパー関数に挙動を任せた部分」**: ホスト fmt に丸投げした `Format`（%T/%p が素通り）、`errors.As` の「first non-nil」近似、map の canonical key の「Def ポインタ同一性」は、単独では正しそうに見えるが組み合わせて初めて壊れる。**差分テストでしか炙り出せない類のバグが今回の収穫の半分**だった。
- **限界**: この手法は「実行結果が決定的に予測できるケース」にしか効かない。真の PBT（プロパティ記述 + ランダム入力 + shrink）とは別物で、カバレッジは列挙したテーマに依存する。型レベルの静的解析系（`go/types` 非使用という制約）や、ホスト境界をまたぐ複雑な参照同一性は別アプローチが要る。

## 7. 計画外: Fuzz-round leftovers 消化ラウンド（round-2）

TODO.md に残っていた §3-4 の lim-\* / 近似項目をこのラウンドで全部向き合わせた。結果は lim-complex / lim-threeidx / lim-bigconst が全て PASS（language corpus 44 件中 40 PASS + 4 PASS-REJECT、DIFF/TRAP/ACCEPT ゼロ）。中心は **untyped constant の遅延具象化モデル** で、ここが計画から一番逸れた設計判断。

### 7.1 `runtime.UConst` — untyped 定数は値ではなく「まだ型を持たない」

`'a'` が `int` と表示される問題・`const B = 1<<100` が未使用でも init 時に死ぬ問題・`1i` が complex 値に落ちない問題は、実は同じ根を持っていた: minigo はリテラルを評価した時点で int64/float64/rune-literal の int64 に **早すぎる具象化** をしていた。Go ではこれらはすべて「untyped constant」であり、**使用される型コンテキストで初めて型が決まる**。

導入したのは `runtime.UConst{V constant.Value, Rune bool}` — `go/constant` の任意精度値を包んだ lazy な値。コンパイラは CHAR / int64 非収容整数 / float64 を溢れる float / IMAG リテラルと、それらを含む fold 済み定数式（`hasCharLit` で rune フレーバを伝播 — `'a'+1` は int32）を `UConst` として emit する。`const` 束縛は ReadOnly セルに `UConst` のまま格納するので `const B = 1<<100` は未使用なら何もしない — Go の「定数宣言は使われて初めて表現可能性を要求される」と一致する。

具象化ポイントは「値に型が要求される場所」すべて:

- `OpNewLocal`/`OpNewGlobal` の `B==0`（非 const 束縛）→ `var x = 'a'` は `Named{rune,int32}` になり `%T` が `int32` と出る
- `var x = 1<<100` → `materializeDefault` が int64/uint64 に収まらず `constant ... overflows int` で trap（Go のコンパイル拒否と同じ文面）
- 型付き `var x T = c` は **OpCoerceTop を bind の前に emit** するよう変更し、`materializeConst(u, td)` がターゲット幅で表現可能性を検査（`var i8 int8 = 300` は trap、`var r MyRune = 'a'` は MyRune に直接変換 — 旧来は default 具象化→Named 不一致 trap だった）
- `assignCell` の無型セル、`convert`（`int8('a')` 等）、`binaryOp`/`unaryOp`（定数域演算→`constBinary`/`constUnary` は `go/constant` の panic を recover で受けて runtime 域にフォールバック）、index/bounds/send/iter/shiftOp、iface coerce
- ホスト境界は `uconstNative`（intrinsics）/`deepHost`/`toReflectValue`/`fmtArg` が受け持つ

### 7.2 complex64/complex128 — `GoValue{complex64|complex128}` で幅を値に持たせる

complex 値は `Named` で包まず **`GoValue` にネイティブの complex64/complex128 を直持ち** させた — Go の幅がそのまま値の Go 型になるので narrow/wide の区別がタグ不要で自明。`type C complex64` の宣言型だけは従来通り `Named` を付ける（`declaredType` 経路）。

- `complex(re, im)` — 引数が float32 風なら complex64、`real`/`imag` は complex64 入力で float32 タグを返す
- 複素数演算は `asComplex`（真の complex のみ検出）+ `complexOperand`（int64/float64 を幅 0＝untyped として昇格）に分離。**幅 0 の昇格定数は両側どちらの幅にも join でき、複素数同士で幅が違えば `mismatched types complex64 and complex128` で trap** — `c64 + c128` も `c64 == c128` も Go のコンパイル拒否と一致する。順序比較 `c < c` は `complex numbers are not ordered`。
- `real(1+2i)` のような定数呼び出しは `constant.Real/Imag` で **定数域のまま** UConst を返す（`real(1e500i)` が `var f float64` でちゃんと溢れる）
- `complexResult` は narrow 側の幅を採用（complex64 wins）

### 7.3 計画から外れた修正ポイント（初回実装で踏んだ罠）

- **`popArgs` の eager materialize を撤回**: 最初は呼び出し境界で UConst を全部具象化したが、これだと `f('a')` が `func(x int)` に入る時点で `Named{int32}` に化けて "cannot use rune as int" で死んだ。Go では `'a'` は untyped のまま int パラメータに入る。`emitParamCoerces` が宣言パラメータ型の OpCoerce を callee prologue に emit する構造なので、args は素のまま渡して coerce に委ねる形に戻した。spread tail も同様。
- **`setIndex` の val materialize を撤回**: `b[0] = 'x'` が []byte 要素の `cannot use rune as byte` で死んだ。elemTypedef 経由の `coerce` が UConst→`materializeConst` を既に担うので、先回り materialize は却って型情報を潰す。
- **`asComplex` が素の int64/float64 に ok=true を返していた**: 初回実装で全数値演算が complex128 化する壊滅的リグレッション（`x*2` が `(20+0i)`）。asComplex を strict に直し、昇格は complexOperand の責務に分離。
- **`append` / `delete` / map リテラルキー / `real`/`imag` に UConst が素通り**: append は elem typedef が分かるなら `v.Call(et, [a])` で変換（VMCaller.Call に typedef を渡せば convert 経路が走る = 追加公開 API 不要）、map リテラルキー・`delete` キーは値ハッシュで合わせるために materialize。
- **mixed const/native の fold**: `const C = B - (1<<99)` の右側はリテラル fold で int64 に落ちるので、`binaryOp` で片側 UConst + 片側 bare literal の時は `constOf` で native を定数域に引き戻して `constBinary`（Go の任意精度と一致 — `B - B` が 0 と評価される）。

### 7.4 残っている近似（このラウンドでも解かなかったもの）

- `f('a')` で `x` 側が Named{int32} に化けた後の `mismatched` は Go と同じ reject 判定だが、メッセージは Go と異なる。
- 定数式の中間値が int64 に落ちるケースは依然ある — `'a' + i`（i は変数）は permissive に計算される（Go は型不一致で reject）。方向は従来通り「緩い側」への一貫したズレ。
- complex の GoValue は `fmt` 等で常に Go ネイティブとして扱われるので、スクリプト側で `%T` の `main.C` 等の表現は Named ラップの有無で変わる（`var c ufC64 = 1+2i` → `main.ufC64` は出るが `complex(1,2)` は `complex128`）。
- `eqlValue`（map キー / switch 内部）は complex を幅無視で数値比較する — Go が reject する `c64 == c128` 相当はここでは受理側に振れる。

### 7.5 回帰テスト

`testdata/fuzzfix/main.go` に `UConstRuneDefault`/`UConstBigConstExpr`/`UConstNamedRune`/`UConstRuneConv`/`PctTDefaults`/`ComplexOps`/`ComplexDecl`/`ComplexMapKey` を追加、`UConstBigTrap`/`UConstFloatOverflow`/`ComplexMixedWidth`/`ComplexOrdered` は `ConstDivZero` 流の trap チェックで検証。全件 `go run` をオラクルに出力一致を確認済み。
