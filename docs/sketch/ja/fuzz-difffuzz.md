# difffuzz ラウンド — 大規模差分潰しのレポート

対象: `podhmo/minigo` の `main`（[fuzz-usecase.md](./fuzz-usecase.md) マージ済みの状態）
方法: `tools/difffuzz`（生成プログラムを `go run` と minigo で流して差分を拾うハーネス）を回し、TODO.md の difffuzz 系 todo を起点に、枯れたら `gen` で補充しながら順に潰すループ。verdict は PASS / TRAP（受理できる差）/ SILENT（バグ）/ CRASH / HANG。修正は「1 PR = 1 根本原因」で、回帰は `testdata/difffuzz/` のピン（`main.go` + `want.stdout`）で固定する。

成果: **53 本の PR（Stack #73、#53–#106）**。TODO.md 記載分 → `$GOROOT/test` コーパス → TRAP バケット → hunt 補充分まで流した。レポート時点で difffuzz の SILENT は全滅、残存 TRAP は全て境界クラス。

## 1. 何をしたか

### フェーズ 1: TODO.md の difffuzz 残件（#53–#63）

fmt・文字列化系の潰し残し。nil composite の描画（`[]`/`map[]`/`[]string(nil)`、`%p`/`%T` の spec 書き換え、要素単位の descent）から、`Sprint` 系の複数値 spread、ホストコールバック内での script panic の再 throw、`slices.Clone`/`maps.Clone` の nil/typedef 保存、`delete` の挿入順リスト掃除、`string(nil スライス)`→`""`、nil スライスの bounds panic メッセージを Go の boundsError テキストに揃える、ホスト sized-int の `Named` 化、`println` の nil ポインタ `0x0`、mixed keyed/positional リテラル、`make` の len/cap 事前チェックまで。

### フェーズ 2: `$GOROOT/test` コーパス（#64–#85）

go 本体の `test/` ディレクトリから拾った「実際の Go プログラム」を byte-identical PASS にするフェーズ。ここが一番重かった。

- **評価順・代入順**: 二相 multi-assign（`a[i], b = f(), g()` で LHS ref を RHS より先に解決、`OpSetRefs`）/ 複数値 return の named result spread / `for x[i] = range` の反復ごと二相代入（#64, #65, #68）
- **定数ドメイン**: 定数比較が真の bool を返す、untyped const が相手の named 型を採用、`len(x[i])`/`cap` の定数畳み込み（`OpLenIdxFold`）、空 const spec の型継承、local const block の iota スロット、UConst を decl storage まで持ち込む const-domain fidelity 一式（#66, #69, #70, #81, #82, #87, #89）
- **interface/型周り**: interface switch の case 厳密性（`any` タグ保持時に `case 1:` が誤マッチする switch.go の本命、`BinEqlIface`）、positional `default:`、無名 struct/func/iface の shape-based assert、uncomparable を値でなく型基準で panic、untyped dynamic value 同士の `==`/`!=`（#71, #76, #77, #90）
- **nil の挙動**: nil `*[N]T` の len/cap/range/index、nil interface メソッド呼び出しの recoverable panic、nil embed 経由 promoted メソッドの選択時 panic、`x = nil` が推論 nilable 型を保持、nil ベースの `&*p`/`&s.f`/`&a[i]`、deferred nil func の呼び出し時 panic（#75, #79, #80, #94, #95, #96）
- **ポインタ同一性**: ゼロサイズ pointee の `runtime.zerobase` 同様の等値、`&a[i]` が backing array 単位で一致（#84, #85）
- **host 境界**: `runtime.Callers`/`CallersFrames`（panic で unwind 済みのフレームも defers 中は参照可能）、`os.Exit`、`io.Writer` script 実装、`runtime/*`/`bytes.Buffer` の corpus 用バインド面、`reflect.DeepEqual`（#80, #71, #72, #86, #88）

このフェーズの終端で corpus 対象ファイル（typeswitch1/reorder/range/divmod/copy/switch/map/nil/recover2/struct0/print/defernil/devirtualization_nil_panics/const3/const4/decl/zerosize/bigalg）は全て byte-identical PASS。

### フェーズ 3: TRAP バケット（#92–#100）

loud な trap から Go の挙動へ寄せる件。

- `swap(swap(a,b))` のような単一呼出引数の結果タプル spread（`callSpread` B=2）
- メソッド式 `T.M`/`(*T).M`/`I.m` の関数値化（宣言メソッドは receiver bind、promoted/iface は再選択 thunk）
- 捕捉変数の `&a` が `OpUpvalRef`、関数ローカル `type` が兄弟宣言の embed spec で解決（`TypeDef.LocalTypes`）
- named 値が identical-underlying の unnamed 先へ代入可能、`Named{T,Named{U,v}}` の peel
- Go 1.20 の slice→array 変換 `[N]T(s)`/`(*[N]T)(s)`（nil は `[0]T` のみ、長さ不足は panic）

### フェーズ 4: hunt 補充分（#101–#106）

`gen` で新しい SILENT を掘るフェーズ。

- **text 深掘り**（seed 3301, `-batches 16 -depth` 深め）: `strings.IndexByte`/`FieldsFunc` の未バインド、そして最後の生き残り「call 引数の評価順」— Go は引数リスト内の全 call（builtin 含む）を lexical order で評価し、index/スライス/変換などの非 call 演算は引数ごとの materialize 時に回す（spec 上 unspecified だが gc は一貫）。`callArgs` を二相化し、非変換の CallExpr・受信を `$arg<N>` に hoist する実装で一致させた（#105）。`&&`/`||` を含む引数は hoist 不能のため sequential フォールバック。
- **num sweep**（seed 8111）: 133 件の SILENT を全滅 — 変換が作る `Named{T,Named{U}}` の二重タグを binaryOp が一段しか剥がず宣言名を失う、`unsignedIntTyp` に `uint` が無く wide unsigned が符号付き描画、`float64`/`float32` 変換が unsigned 源を int64 符号読み、unary `-`/`^` がタグ付き wide box を `GoValue` に落とす、の 4 系統（#102–#104）。ガード再実行 0 SILENT。
- **usecasefuzz リグレッション**: `[]byte(constString)` が `cannot use constant as []byte` で trap（#106 で修正、詳細は §3）。

## 2. 残りの状況

- **difffuzz**: SILENT はゼロ。残る TRAP は全て「境界クラス」:
  - `unsafe.Pointer` 系 22 プログラム — ポインタの値モデル自体が要る大物（[#40](https://github.com/podhmo/minigo/issues/40) で管理）
  - GC fidelity（`SetFinalizer`/`MemStats` 等はバインド済みだが真の GC は無い）
  - スクリプト値の `reflect.ValueOf`、`unsafe.String`/`Offsetof`
  - `gcgort`（そもそも Go でも本物のデッドロックを起こすプログラム）
  - copy/divmod のスループット HANG（時間計測差分）
- **usecasefuzz**: 33 PASS / 1 ACCEPT（`inspectuse` は minigo 独自機能の意図的なもの）/ 4 TRAP。TRAP は全て `lim-*` の境界プローブ（http/toml/xml/yaml — バインド外 stdlib・サードパーティの reflect 内部に踏み込んで init で落ちる様子の観測用）。**今回のラウンドでリグレッションなし**（一時的に生じた `[]byte(UConst)` は本ラウンド内で検出・修正済み）。
- **ハーネス TODO**: usecasefuzz 型の手書きシナリオ用 skill（difffuzz の verdict 契約と `testdata/difffuzz` 回帰形式を共有するもの）— TODO.md に残置、今回は未着手。
- **既知の差分（受理したもの）**: `&&`/`||` を含む call 引数は sequential 評価にフォールバック; `SelectorExpr` 経由の変換（`time.Duration(x)` 等）はホスト呼出しとして不透明扱い; 二相評価は call 引数のみで `return` 式や composite literal 要素は対象外（同型の差分が将来出うる）。

## 3. 改めて考える実装の不備

- **型タグ（`runtime.Named`/`UConst`）の付与・剥離が分散して一貫しなかった**。conversion が `Named{T,Named{U}}` を作る、`binaryOp` が一段しか剥がない、`unaryOp` が `GoValue` 化でタグを落とす、`coerceConcrete` の sized-int スロットだけタグを付け忘れる…同じ「値がその宣言型を覚えているか」という関心事が 5 箇所以上で独立に実装されていて、片方を直すと他方で二重タグ/タグ落ちが出る。`UConst` 導入で「宣言時に型を確定しない値」が増えた後、この分散はさらに顕在化した（後述のリグレッション）。「剥がす」「付ける」「読む」の 3 操作を一本化する共通ヘルパの入口を設けるのが筋だった。
- **評価順序が「各引数を完全に評価する」前提で書かれていた**。Go（gc）の二相順序（call を lexical に先、非 call 演算を後で引数ごと）は spec 上 unspecified なので当初は差分として受理対象にも見えたが、実際には corpus が panic メッセージ順まで一致を要求するので追随せざるを得なかった。宣言代入・return・引数で評価戦略が揃っていなかったこと自体が不備。
- **ホスト境界のバインドが逐次手当て**。`runtime.Callers`、`strings.IndexByte`、`Fprint` 系、`time.Duration`、`os.Args` の変数化…1 件ずつ TRAP を潰す形になった。「Go のプログラムが触れる stdlib 面を先に棚卸しする」視点があれば hunt 段取りを変えられた。
- **panic ペイロードと error 型の接続**が後手。`recover().(error).Error()` が効かない、uncomparable を値でなく型で判定する、assert 失敗を `GoValue{error}` に、など「panic を Go の error として処理できるか」が散布していた。

## 4. 計画外の状況とその時の意思決定

- **usecasefuzz でのリグレッション発覚（最重要）**: 依頼された「usecasefuzz も実行しておいて」で `[]byte(const)` の trap が出た。原因は本ラウンドの #89（const-domain fidelity — UConst を decl storage まで持ち込む）の副次影響で、`materializeConstErr` が非 basic ターゲットで `cannot use constant` を返すだけだった。ユーザーのリグレッション確認要請がそのまま検知器として機能した。判断: TODO 補充停止の指示直後だったが「自分が入れたリグレッションは別の仕事ではない」として即座に別 PR（#106）で修正。UConst を default 型に materialize してから通常変換パスに流す最小修正に留めた。
- **Stacked PR 化**: 途中で「Stacked PR にできる？」と指示。各 PR の base を前の devin ブランチにしてあったため、そのまま Stack #73 として 53 本を登録できた。以後は新 PR の base をスタックトップにして `git_stack add` で追加、マージ・base 変更は GitHub 側の retarget に任せる運用に移行。
- **eval-order を直すかの判断**: spec unspecified の差分なので「既知差分として記録して終わる」選択肢もあったが、corpus の panic メッセージ一致を考えると残置すると毎回 hunt で再浮上するため修正を選択。ただし `&&`/`||` 引数の hoist は副作用順序を壊しうるので sequential フォールバックを残す、という「完全追随しない範囲」を明示する設計にした。
- **1 PR の粒度**: 原則は 1 根本原因 1 PR だが、corpus 1 ファイルを PASS にする複数修正（map.go の 3 修正、nilptr2.go の 4 修正）は同一ファイルの PASS 達成という単位で束ねた。レビュー容易性より「各 PR が corpus verdict を動かす」単位を優先した判断。
- **作業ミスの記録**: `convarray` のコミットが一時 `assignnamed` ブランチに混入 → soft reset + force-with-lease で分離（自分のブランチのみ、共有前）。`testdata` の gofmt ドリフト（mapclone）が `make format` のたびに再発するため commit 前に `git checkout` で戻す運用に落ち着いた。
- **Scope 判断**: `gcgort` は Go でも実際にデッドロックする、copy/divmod の HANG はスループット差 — 「Go と同じ振る舞いをすること自体が目的に合わない」ケースとして TRAP 受理に留め、unsafe.Pointer 系はポインタ値モデルが要る大物として issue 管理（#40）に逃がした。
