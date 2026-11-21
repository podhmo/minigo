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

## P4-2a の計測（`TestSpikeSlotClassification`、difffuzz corpus 全体）

コンパイラが `Chunk.Boxed` に記録した分類:

| 指標 | 値 |
|---|---:|
| 分類された chunk | 4722 |
| boxed スロットを持つ chunk（= escape） | 872（18.5%） |
| ローカルスロット総数 | 12247 |
| boxed スロット | 1634（13.3%） |

事前の推定（レシーバ解決を観測値に限った正確な規則で 9.3%）より高い。
差の主因はレシーバが不明なときの保守的な Boxed で、`x := f()` の結果型が
推論で解けない operand や promoted member はすべて Boxed になる。
10% への絞り込みはレシーバ解決の精度（declTyp の伝播）がボトルネックであり、
P4-2c で扱う。

## P4-2b の表現（`frame.locals []runtime.Value`）

`locals` はスロット配列で、boxed スロットには非公開ラッパー
`*localCell`、plain スロットには生の値が入る。`*runtime.Cell` を
直接要素にせずラッパーを噛ませるのは必須で、plain スロットの値が
`&x` の ref（それ自体が `*runtime.Cell`）になり得るため、要素の型では
スロット種別を判定できない。ラッパーはフレーム外に出ないので
`f.slotRefs` ブロックとして常にリサイクルする。

- boxed スロットの cell は従来どおり 1 ブロック確保するが、
  「escape しない chunk」= boxed が無い chunk は cell をゼロ確保
  する（元の最適化の完全版）。escape する chunk は boxed 数だけの
  新規ブロックを確保する。
- plain スロットの宣言型は `f.ltyps[i]` に置く（`OpCoerce` が書き、
  `OpLocalTyp`/代入 coerce が読む）。box 化は `ReadOnly`（const）と
  `localCell` による on-demand 昇格（compiler が見落とした ref/
  capture 用）だけが要求する。
- `OpNewLocal`/`OpRenewVar` は boxed スロットに従来どおり新しい
  cell を入れる（capture の detach を維持）。plain スロットは
  `valueCopy` の直接書き込みか no-op。
- `assignLocal` は `assignCell` と同じ store 規則を plain スロットの
  値と `ltyps` に対して評価する（`retypeStore` に抽出）。配列の
  in-place overwrite（`overwriteArrayIn`）は slice view のために
  保持する。
- `specialCtx.Eval` の shared-locals コピーは同じ配列型をコピー
  するだけで、参照可能なローカルは全部 boxed なので共有 cell
  ポインタとして届く。

## P4-2c の分類規則（証明できる escape のみ事前 box）

P4-2a の初版は「レシーバを証明できないものは全部 box」だったが、
P4-2b で plain スロットが `localCell` による on-demand 昇格を得た
ため、見落としのコストは「先確保するか実行時に 1 回確保するか」の
違いだけになった。そこで分類を反転し、**コンパイル時に escape を
証明できるスロットだけを box** する。ref を取る全経路
（`OpLocalRef`/`OpMakeClosure`/`OpSelectCall` 経由）は plain
スロットでも `localCell` が昇格するので、偽陰性は常に正しい。

証明できるもの（box する）:

- `&x`・`&x.f` の ref と upvalue 参照（"ref"/"fieldref"）
- closure キャプチャ（"capture"）と特殊形式の見えるローカル
  （"special"）
- ポインタレシーバメソッドの select — `x.M()`/`x.M` で index の
  宣言から M のレシーバが `*T` と確定するもの（"select:ptrrecv"）。
  embedded フィールドは再帰的に掘る（`S` に `U` 埋め込みのとき
  `s.PP()` は U の decl に降りて判定する）。`type A = T` エイリアス
  と `pkg.T` も index 経由で同じ判定を受ける。

証明できないもの（plain のまま、必要なら実行時に昇格）:

- `x.M()` の x の declTyp が推論で解けない（`x := f()` で結果型が
  得られない束縛。difffuzz 上で最大の発生源だった）
- promoted member で embedded 先が index に無い、型パラメータ
  レシーバ、index 上に無い型名、深度超過のエイリアス連鎖

型推論側も 2 点補強した: calleeFuncTyp は `fs[0]()` のように
宣言外の callee をオペランドの静的型（`func() T` 形）から引く。
また `x := fs[0]()` のような `m[k]` 経由でも結果型が束縛先に
届くようになった。

同一性の検証（difffuzz `lazypromote`）: `x := id(C{})` のように
declTyp が解けない束縛は plain のまま `x.Bump()` で昇格し、後続の
`&x` や closure キャプチャと同じ cell を共有する（`*p`/`x`/closure
の三経路で同じ N を読む）。

### P4-2c の計測（同一 corpus = difffuzz 全件）

| 指標 | P4-2a（保守的） | P4-2c（証明のみ） |
|---|---:|---:|
| boxed スロットを持つ chunk | 876（18.5%） | 433（9.1%） |
| boxed スロット | 1641（13.4%） | 721（5.9%） |

残る box の内訳は capture 415、ref 257、ptrrecv 26、
fieldref 23、special。P4-2 の親 TODO が目標とした「escape する chunk
~10%」を達成した。run-time の昇格（escaping ref の事後確認）は
424 件で、全部コンパイル時分類が追い越した経路。
