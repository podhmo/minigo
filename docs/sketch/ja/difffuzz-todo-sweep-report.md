# difffuzz TODO sweep レポート — 大規模差分潰し

対象: `podhmo/minigo` の `main`（[fuzz-usecase.md](./fuzz-usecase.md)・[difffuzz-harness.md](./difffuzz-harness.md) までの資産を引き継いだ状態）
方法: `tools/difffuzz`（生成プログラムを `go run` と minigo で流して差分を拾うハーネス）を回し、TODO.md の difffuzz 系 todo を起点に、枯れたら `gen` で補充しながら順に潰すループ。verdict は PASS / TRAP（受理できる差）/ SILENT（バグ）/ CRASH / HANG。修正は「1 PR = 1 根本原因」で、回帰は `testdata/difffuzz/` のピン（`main.go` + `want.stdout`）で固定する。

成果: **54 本の PR（GitHub Stack #73、#53–#107）**。TODO.md 記載分 → `$GOROOT/test` コーパス → TRAP バケット → hunt 補充分まで流した。difffuzz の SILENT は全滅、残存 TRAP は全て境界クラス。本ラウンドの usecasefuzz 再実行: 33 PASS / 0 DIFF / 1 ACCEPT / 4 TRAP（全て lim-* 境界プローブ、http/toml/xml/yaml）— **リグレッションなし**（途中で生じた `[]byte(UConst)` はラウンド内で検出・修正済み、§4 参照）。

## 1. 何をしたか

- **フェーズ 1: TODO.md の difffuzz 残件（#53–#63）** — fmt・文字列化系。nil composite の描画（`[]`/`map[]`/`[]string(nil)`、要素単位 descent、`%p`/`%T` 書き換え）、`Sprint` 系の複数値 spread、ホストコールバック内 script panic の再 throw、`slices.Clone`/`maps.Clone` の nil/typedef 保存、`delete` の挿入順掃除、`string(nil スライス)`→`""`、nil スライスの boundsError テキスト、ホスト sized-int の `Named` 化、`println` nil `0x0`、mixed keyed/positional リテラル、`make` len/cap 事前チェック。
- **フェーズ 2: `$GOROOT/test` コーパス（#64–#85）** — 一番重かった。二相 multi-assign（`OpSetRefs`）と named result spread、反復ごとの range 代入、定数ドメイン一式（定数比較が真の bool、untyped const の相手型採用、`OpLenIdxFold`、空 const spec 型継承、local iota、decl storage までの UConst 持ち込み）、interface switch の厳密一致（`BinEqlIface`）、shape-based 無名型 assert、型基準 uncomparable、nil `*[N]T`、nil iface 呼び出しの recoverable panic、nil embed promoted メソッド、`x = nil` の型保持、nil ベース address-of、deferred nil func、zerobase 等値、`&a[i]` の backing array 同一性、`runtime.Callers`/`CallersFrames`（unwind 済みフレームも defer 中は参照可）、`os.Exit`、script `io.Writer`、`runtime/*`+`bytes.Buffer` バインド、`reflect.DeepEqual`。終端で対象コーパス全ファイル byte-identical PASS。
- **フェーズ 3: TRAP バケット（#92–#100）** — 単一呼出引数の結果タプル spread（`callSpread` B=2）、メソッド式 `T.M`/`(*T).M`/`I.m` の関数値化、捕捉変数の `OpUpvalRef`、関数ローカル `type` の embed spec 解決（`TypeDef.LocalTypes`）、identical-underlying unnamed への named 代入、`Named{T,Named{U}}` peel、Go 1.20 slice→array 変換。
- **フェーズ 4: hunt 補充分（#101–#106）** — `strings.IndexByte`/`FieldsFunc` バインド; call 引数評価順の二相化（call は lexical に先、index/変換など非 call 演算は引数ごとの materialize 時 — spec 上 unspecified だが gc は一貫、`$arg<N>` hoist 実装、`&&`/`||` 引数は sequential フォールバック）; num sweep（seed 8111）の 133 SILENT 全滅（二重 Named peel、unsigned ドメイン、unary タグ落ち、幅のある const 変換）。ガード再実行 0 SILENT。

## 2. 残りの状況

- **difffuzz**: SILENT ゼロ。残る TRAP は全て境界クラス — `unsafe.Pointer` 系 22 プログラム（[#40](https://github.com/podhmo/minigo/issues/40) で管理）、GC fidelity（`SetFinalizer`/`MemStats` はバインド済みだが真の GC なし）、script 値の `reflect.ValueOf`、`unsafe.String`/`Offsetof`、`gcgort`（Go でも本物のデッドロック）、copy/divmod のスループット HANG。
- **usecasefuzz**: 33 PASS / 1 ACCEPT（`inspectuse`）/ 4 TRAP。残りは `lim-http`/`lim-toml`/`lim-xml`/`lim-yaml` — バインド外 stdlib・サードパーティの reflect 内部に踏み込む意図的プローブ。
- **ハーネス TODO**: usecasefuzz 型手書きシナリオ用 skill — TODO.md 残置、未着手。
- **既知の受理差分**: `&&`/`||` を含む call 引数は sequential 評価; `SelectorExpr` 経由変換（`time.Duration(x)`）はホスト呼出し不透明扱い; 二相評価は call 引数のみで `return` 式・composite literal 要素は対象外。

## 3. 改めて考える実装の不備

- **型タグ（`runtime.Named`/`UConst`）の付与・剥離が分散して一貫しなかった**。conversion が `Named{T,Named{U}}` を作る、`binaryOp` が一段しか剥がない、`unaryOp` が `GoValue` 化でタグを落とす、`coerceConcrete` の sized-int スロットだけタグ忘れ…「値が宣言型を覚えているか」が 5 箇所以上で独立実装されていて、片方を直すと他方で二重タグ/タグ落ちが出た。UConst 導入後にさらに顕在化（§4）。「剥がす」「付ける」「読む」の 3 操作を一本化する共通入口が筋だった。
- **評価順序が「各引数を完全に評価する」前提だった**。gc の二相順序（call を lexical に先、非 call 演算を後で引数ごと）と乖離し、corpus が panic メッセージ順まで一致を要求するため追随せざるを得なかった。宣言代入・return・引数で評価戦略が揃っていなかったこと自体が不備。
- **ホスト境界のバインドが逐次手当て**。`runtime.Callers`、`strings.IndexByte`、`Fprint` 系、`time.Duration`、`os.Args` 変数化…1 件ずつ TRAP を潰す形。「Go プログラムが触れる stdlib 面を先に棚卸しする」視点があれば段取りを変えられた。
- **panic ペイロードと error 型の接続が後手**。`recover().(error)` が効かない、uncomparable を値でなく型で判定、assert 失敗を `GoValue{error}` に、など「panic を Go の error として処理できるか」が散布していた。

## 4. 計画外の状況とその時の意思決定

- **usecasefuzz でのリグレッション発覚（最重要）**: 実行依頼で `[]byte(const)` の trap が出た — 本ラウンド #89（UConst を decl storage まで持ち込む）の副次影響で、`materializeConstErr` が非 basic ターゲットで `cannot use constant` を返すだけだった。リグレッション確認要請がそのまま検知器として機能した。判断: TODO 補充停止の指示直後だったが「自分が入れたリグレッションは別の仕事ではない」として即座に別 PR（#106）で修正 — UConst を default 型に materialize してから通常変換パスに流す最小修正に留めた。
- **Stacked PR 化**: 途中指示で全 PR を Stack #73 として登録。各 PR の base を前の devin ブランチにしてあったため移行は自然だった。以後は新 PR の base をスタックトップにして `git_stack add`、base 変更・マージは GitHub の retarget 任せに移行。
- **eval-order を直すかの判断**: spec unspecified の差分なので「既知差分として記録して終わる」選択肢もあったが、corpus の panic メッセージ一致のため毎回 hunt で再浮上するので修正を選択。`&&`/`||` 引数の hoist は副作用順序を壊しうるので sequential フォールバックを残す「完全追随しない範囲」を明示した。
- **1 PR の粒度**: 原則は 1 根本原因 1 PR だが、corpus 1 ファイルを PASS にする複数修正（map.go 3 修正、nilptr2.go 4 修正）は「同一プログラムを PASS にする」単位で束ねた。レビュー容易性より「各 PR が corpus verdict を動かす」単位を優先した判断。
- **作業ミスの記録**: `convarray` コミットが一時 `assignnamed` ブランチに混入 → soft reset + force-with-lease で分離（自分のブランチのみ、共有前）。`testdata` の gofmt ドリフト（mapclone）が `make format` のたびに再発するため commit 前 `git checkout` で戻す運用に落ち着いた。
- **Scope 判断**: `gcgort` は Go でも実際にデッドロック、copy/divmod の HANG はスループット差 — 「Go と同じ振る舞い」が目的に合わないケースとして TRAP 受理。unsafe.Pointer 系はポインタ値モデルが要る大物として #40 に管理委譲。
