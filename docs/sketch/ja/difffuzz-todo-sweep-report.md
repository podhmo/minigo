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

## 5. 次ラウンドの再開方法

次の sweep を始めるときのプロンプト（このラウンドの教訓を反映した版）:

```
@podhmo/minigo difffuzz harness（tools/difffuzz、.claude/skills/difffuzz/SKILL.md）を使って、difffuzz の todo を確認しながら潰していってください。TODO.md の残件から始め、枯れたら `go -C ./tools/difffuzz run ./ gen` で hunt して新しい SILENT を補充してください。

- 1 根本原因 = 1 PR。回帰は testdata/difffuzz/<verdict>_<slug>/（main.go + want.stdout）にピンする
- 修正 PR は stacked PR にして（各 PR の base を前の devin ブランチにして `git_stack` で既存の stack に追加、無ければ新規作成。マージ・base 変更は GitHub の retarget 任せで自分では行わない）
- 境界クラスは潰し対象外: unsafe.Pointer → issue #40、GC fidelity、script 値の reflect.ValueOf、unsafe.String/Offsetof、gcgort（Go でもデッドロック）、スループット差の HANG
- 最後に usecasefuzz（github.com/podhmo/minigo-usecasefuzz、`MINIGO_DIR=<checkout> bash run.sh`）を回してリグレッション確認
- 終了時に docs/sketch/ja/ にレポート（実施内容・残り状況・不備の振り返り・計画外の記録と判断）
```

補足: 残存 TODO が無い状態からの再開になるので、hunt が実質の入口になる（`-domain text -seed <新 seed> -batches 16 -per-bucket 1`、深く掘るなら `-batches 32 -depth 6`）。num は seed 8111 で 0 SILENT 済み。

## 6. レビュー指摘への対応とリファクタリング提案の評価

### 6.1 指摘された5件の修正

別エージェントのレビューで本ラウンドの変更由来の diverge が5件報告された。全て `go run` との差分を確認の上で修正（[#108](https://github.com/podhmo/minigo/pull/108)–[#111](https://github.com/podhmo/minigo/pull/111)、Stack #73 の続き）。

- **len/cap 添字畳み込みの前提不足（#108、指摘2件を同所に集約）** — `len(a[idx()])` が `idx()` を評価しなかった（operand に call/受信がある場合 Go は評価する）。さらにユーザー宣言の `func len(a [3]int)` も名前だけで畳み込まれ呼ばれなかった。`!c.declared(id.Name)`（builtin 解決ガード）と `hoistedArgCalls`（call/受信検出、引数二相化で導入済みの分類器を再利用）で fold をガード。
- **iota 束縛のブロック横断（#109）** — slot は関数スコープだが名前束縛は最初のブロックだけ。`{const A = iota}; {const B = iota}` で2つ目が `undefined: iota` に trap。slot 再利用時に現在ブロックへ `iota→slot` を再バインド（ユーザー宣言の shadow はそのまま優先）。
- **代入先の nil チェック時期（#110）** — `OpIndexRef`/`OpFieldRef` が ref 生成時に nil base チェックを行い、`s[0] = before()` で RHS が評価されなかった。Go は phase-2 の store 時に panic。store ターゲット（B=1）では eager check をスキップし `setIndex`/`setField` の panic に委譲（`*[N]int` nil base の arm も追加）。
- **DeepEqual の型一致（#111）** — 配列も slice も `runtime.Slice` で、`Named`/ref 層が先に剥がれていたため `DeepEqual([]int{1}, [1]int{1})` が true。`Named` タグと deref 層を両辺 lockstep で剥がして型不一致を早期判定し、複合型は typedef の AST spelling（`deepTypeEq`、`[]int` vs `[1]int` vs `[]string` を区別）で比較。

いずれも「最適化・新規 op が spec 上の前提条件を検査していなかった」系で、§3 の「評価順序前提」反省と同根。fold・ref・unwrap を入れるときは「どの条件で素朴な経路と等価になるか」をガードに落とす形が必要。

### 6.2 リファクタリング提案の妥当性

提案3件をコード上の実態と照合した。結論: 3件とも妥当 — ただし優先度と粒度が異なる。修正系の sweep とは別 PR・別ラウンドで扱うのが良い。

1. **`runtime.Map` の insert/delete/clear をメソッド集約 — 妥当、実害あり（中〜高）**。検証したところ `Order`/`Keys`/`Pairs` の三連 append イディオムが vm.go に3箇所（setIndex、literal 構築、GoValue unbox）、intrinsics.go に2箇所（copy、native→script 変換）、delete が builtins.go に散在し、`CanonicalKey` の計算も各所で重複。#74 で入れた NaN nonce キーは「canonical key の生成方針」そのもので、insert ごとに正しく適用されているかは各サイトの実装依存になっている。`Map.Insert(k, v)`/`Delete`/`Clear` + key 正規化を1箇所に閉じ込めれば NaN 方針も強制できる。更新漏れリスクが実際に存在するので、正当性の提案。
2. **compiler の型・名前解決の共通化 — 妥当、中（一貫性効果）**。`declared`（任意の宣言）・`isTypeName`（型名のみ、非型 decl による shadow を考慮）・`isIfaceTypeExpr`・`isKeyedLitShape` は `fs→binds→pkg.Index→predeclared` の同じラダーを歩くが、判定したい属性が違うので「解決結果」を返す共通関数（`resolveName → {found, kind, spec}`）にするのが正しい方向。実際 `isIfaceTypeExpr` は `index.Types` を見るが local var による shadow を見ていない — `var Reader` が `type Reader interface` を覆うケースで誤判定し得る小さな latent divergence で、ラダーの手書き複製の弊害が既に出ている。今回の len/cap 修正で `declared` をガードに使ったのもこの系の一例。
3. **`RuntimeError` 生成の共通ヘルパー — 妥当、低リスク（整合性効果）**。`&runtime.Panic{Value: &runtime.RuntimeError{Msg: ...}}` の組み立てが vm.go+builtins.go で40箇所。recover の `.(error)` が効くかはペイロードの型依存（#55/#76 で直した系）で、文字列 payload と混在すると静かに壊れる。`runtime` 側に `BoundsError(i, n)` 系コンストラクタ、vm 側に `panicRuntime` 系の一括経路を置けばメッセージ形式と wrapper の一貫性が担保できる。ただしメッセージ文言がサイトごとに違うので「完全な一本化」より「wrapper 生成だけ共通化」の粒度が適切。

補足: いずれも挙動中立の整理なので、diverge 修正と混ぜるとレビューが追いにくい。ピン済みの corpus が緑のまま通ることを確認しながら別 PR で進めるのが安全。
