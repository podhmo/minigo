# difffuzz TODO sweep レポート — 大規模差分潰し

> 注記: このレポート PR は Stack #73 の中間位置にある。§6 に記した修正（#108–#119 等）は本 PR より**上位**の積層ブランチにあり、このブランチのツリーを checkout しても修正コードは含まれない。検証する場合はスタックトップ（`devin/1790987393-switchdefault` 以降）を使うこと。

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

**レビュー後の追補（§6 までの対応を踏まえて）**: 上記4項目は反証なしで有効のままだが、レビュー6ラウンドで新しい不備クラスが3つ見えた。

- **名前解決メタデータの寿命 ≠ lexical ブロックスコープ**。iota slot（#109）、終了ブロックの型宣言残留（#123）、内側 type 宣言の上書き（#126）。関数単位フラット map に置いていたメタデータがブロックの生死に追随しなかった系。fscope の per-block 化で構造的には解消したが、「binding の寿命」を設計上の第一概念にしていなかったのが根因。
- **最適化・fold の前提条件ガード不足**。len/cap fold が shadow（#108）・副作用・operand base（#122）を見逃した系。素朴経路と等価になる条件を明示しない変換は spec 差分を静かに生む。§3 の評価順序不備と同根だが、発現点が「評価順序そのもの」から「変換適用可否の判定」に広がった。
- **lvalue が「値スナップショット」と「live ストレージ参照」を区別しなかった**。`s[i] = rhs()` の bounds 先読み（#110）で兆候、#127 で確定。Go の代入先は格納時に解決する参照チェーンで、key のみ評価時スナップショット。`DerefRef`・`refTargetBase` の導入で ref 型族に「遅延解決の location」が加わった — 評価順序不備の下にあった、より深い構造不備。

なお §3 で挙げた改善方向（タグ操作の共通入口・stdlib 面の棚卸し・panic ペイロード統一）は現時点でも未実施のまま残っている — §6.2 のリファクタリング評価と合わせて別ラウンドの候補。

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
- レポート・docs の PR は必ずスタックの**最上位**に作る（中間に置くと、そのブランチを checkout した検証者に上位の修正が見えず「未修正」と誤報される — 本ラウンドで発生）
- `git_stack unstack` は指定 PR だけでなく**スタック全体を解体**するので使わない（順序変更が必要なら `git_stack create` に全 PR の順序リストを渡して作り直す）
- レビュー指摘は現スタックトップで再現を確認してから直す（レビューのベースが古いことが多く、今回 3/5・次回 5/7 が既修正だった）
- 境界クラスは潰し対象外: unsafe.Pointer → issue #40、GC fidelity、script 値の reflect.ValueOf、unsafe.String/Offsetof、gcgort（Go でもデッドロック）、スループット差の HANG
- 最後に usecasefuzz（github.com/podhmo/minigo-usecasefuzz、`MINIGO_DIR=<checkout> bash run.sh`）を回してリグレッション確認
- 終了時に docs/sketch/ja/ にレポート（実施内容・残り状況・不備の振り返り・計画外の記録と判断）
```

補足: 残存 TODO が無い状態からの再開になるので、hunt が実質の入口になる（`-domain text -seed <新 seed> -batches 16 -per-bucket 1`、深く掘るなら `-batches 32 -depth 6`）。num は seed 8111 で 0 SILENT 済み。

## 6. レビュー指摘への対応とリファクタリング提案の評価

運用メモ: この `## 6.N` 系列は各ラウンドの記録を1章ずつ積んだもの（見出しは当初「レビュー指摘への対応…」で始まったため番号だけが通しになっている）。新しいラウンドは直前の番号に続けて `## 6.<next>` を追記する。`## 1`–`## 5` は最初のラウンド固有の節。見出しテキストは `§6.N` の相互参照とアンカーを維持するため変更しない。

## 6.1 指摘された5件の修正

別エージェントのレビューで本ラウンドの変更由来の diverge が5件報告された。全て `go run` との差分を確認の上で修正（[#108](https://github.com/podhmo/minigo/pull/108)–[#111](https://github.com/podhmo/minigo/pull/111)、Stack #73 の続き）。

- **len/cap 添字畳み込みの前提不足（#108、指摘2件を同所に集約）** — `len(a[idx()])` が `idx()` を評価しなかった（operand に call/受信がある場合 Go は評価する）。さらにユーザー宣言の `func len(a [3]int)` も名前だけで畳み込まれ呼ばれなかった。`!c.declared(id.Name)`（builtin 解決ガード）と `hoistedArgCalls`（call/受信検出、引数二相化で導入済みの分類器を再利用）で fold をガード。
- **iota 束縛のブロック横断（#109）** — slot は関数スコープだが名前束縛は最初のブロックだけ。`{const A = iota}; {const B = iota}` で2つ目が `undefined: iota` に trap。slot 再利用時に現在ブロックへ `iota→slot` を再バインド（ユーザー宣言の shadow はそのまま優先）。
- **代入先の nil チェック時期（#110）** — `OpIndexRef`/`OpFieldRef` が ref 生成時に nil base チェックを行い、`s[0] = before()` で RHS が評価されなかった。Go は phase-2 の store 時に panic。store ターゲット（B=1）では eager check をスキップし `setIndex`/`setField` の panic に委譲（`*[N]int` nil base の arm も追加）。
- **DeepEqual の型一致（#111）** — 配列も slice も `runtime.Slice` で、`Named`/ref 層が先に剥がれていたため `DeepEqual([]int{1}, [1]int{1})` が true。`Named` タグと deref 層を両辺 lockstep で剥がして型不一致を早期判定し、複合型は typedef の AST spelling（`deepTypeEq`、`[]int` vs `[1]int` vs `[]string` を区別）で比較。

いずれも「最適化・新規 op が spec 上の前提条件を検査していなかった」系で、§3 の「評価順序前提」反省と同根。fold・ref・unwrap を入れるときは「どの条件で素朴な経路と等価になるか」をガードに落とす形が必要。

## 6.2 リファクタリング提案の妥当性

提案3件をコード上の実態と照合した。結論: 3件とも妥当 — ただし優先度と粒度が異なる。修正系の sweep とは別 PR・別ラウンドで扱うのが良い。

1. **`runtime.Map` の insert/delete/clear をメソッド集約 — 妥当、実害あり（中〜高）**。検証したところ `Order`/`Keys`/`Pairs` の三連 append イディオムが vm.go に3箇所（setIndex、literal 構築、GoValue unbox）、intrinsics.go に2箇所（copy、native→script 変換）、delete が builtins.go に散在し、`CanonicalKey` の計算も各所で重複。#74 で入れた NaN nonce キーは「canonical key の生成方針」そのもので、insert ごとに正しく適用されているかは各サイトの実装依存になっている。`Map.Insert(k, v)`/`Delete`/`Clear` + key 正規化を1箇所に閉じ込めれば NaN 方針も強制できる。更新漏れリスクが実際に存在するので、正当性の提案。
2. **compiler の型・名前解決の共通化 — 妥当、中（一貫性効果）**。`declared`（任意の宣言）・`isTypeName`（型名のみ、非型 decl による shadow を考慮）・`isIfaceTypeExpr`・`isKeyedLitShape` は `fs→binds→pkg.Index→predeclared` の同じラダーを歩くが、判定したい属性が違うので「解決結果」を返す共通関数（`resolveName → {found, kind, spec}`）にするのが正しい方向。実際 `isIfaceTypeExpr` は `index.Types` を見るが local var による shadow を見ていない — `var Reader` が `type Reader interface` を覆うケースで誤判定し得る小さな latent divergence で、ラダーの手書き複製の弊害が既に出ている。今回の len/cap 修正で `declared` をガードに使ったのもこの系の一例。
3. **`RuntimeError` 生成の共通ヘルパー — 妥当、低リスク（整合性効果）**。`&runtime.Panic{Value: &runtime.RuntimeError{Msg: ...}}` の組み立てが vm.go+builtins.go で40箇所。recover の `.(error)` が効くかはペイロードの型依存（#55/#76 で直した系）で、文字列 payload と混在すると静かに壊れる。`runtime` 側に `BoundsError(i, n)` 系コンストラクタ、vm 側に `panicRuntime` 系の一括経路を置けばメッセージ形式と wrapper の一貫性が担保できる。ただしメッセージ文言がサイトごとに違うので「完全な一本化」より「wrapper 生成だけ共通化」の粒度が適切。

補足: いずれも挙動中立の整理なので、diverge 修正と混ぜるとレビューが追いにくい。ピン済みの corpus が緑のまま通ることを確認しながら別 PR で進めるのが安全。

## 6.3 レビュー第2ラウンド: 5件の修正とリファクタリング評価

第2ラウンドのレビューでさらに5件が報告された。全て `go run` との差分を確認の上で修正（[#113](https://github.com/podhmo/minigo/pull/113)–[#117](https://github.com/podhmo/minigo/pull/117)、Stack #73 積み増し）。

- **nil slice → 非ゼロ長配列変換（#113）** — `convertArray` の nil-slice arm が無条件でゼロ配列を返していた。`var s []int; _ = [1]int(s)` は Go では長さ不足の runtime panic。`n > 0` で panic、`n == 0` のみゼロ配列に。
- **DeepEqual の typed nil 同一性（#114）** — nilish 判定が `Typ.Name` だけを見ていたため `(*int)(nil) == (*string)(nil)`、`[]int(nil) == nil` が true。`runtime.TypedNil` 同士は `deepTypeEq`（kind + AST spelling）で比較し、untyped nil / empty-iface nil 同士は「ただの nil」として相等 — 実機で `DeepEqual(io.Reader(nil), nil) == true`、`io.Reader(nil) == io.Writer(nil) == true` を確認して分岐を設計した。
- **DeepEqual の map key 照合（#115）** — key を deepEql していたため、pointee が等しい別アドレスの pointer key が一致扱い（Go では map lookup の等値性＝ポインタ同一性）。`Pairs` の canonical key で `bm.Pairs[ak]` を直接引く形に変更し、value だけを再帰比較 — canonical key は map の等値性そのものをエンコードしているので仕様と一致。
- **`%p`/`%T` の共有 operand 破壊（#116）** — rewriteTypeVerbs が `a[off+pos]` を直接上書きするため `fmt.Printf("%v %[1]p", p)` が `0x… 0x…` に。directive を `{pos, verb, stars}` の中間表現で収集し、spec を sequential 化（`[n]` 剥がし）+ arg tail を作り直す構成に変更 — 各 verb が専用 operand を持つので共有 slot は消えた。ついでに `%[n]` 後の implicit-arg カウンタ（`argNum = n`）と `*` operand の consumption、`%!(EXTRA …)` の高水位保持も Go 準拠に。
- **`runtime.Frames.Next` の more（#117）** — 最終フレームでも `more=true` 固定だったため canonical ループが空フレームを余計に処理。`cf.i < len(cf.sites)` を返す。加えて `text_pass_nilpanic` pin の `for f, next := Next(); next;` イディオム自体が最終フレームを読み落とすバグだった（Go では runtime フレームが後ろに居て隠れていた）ので正規形に修正。

### リファクタリング提案の評価（第2ラウンド）

1. **`deepEql` を型比較・pointer 比較・値比較のフェーズ分割 — 妥当だが、大半は第1〜2ラウンドの修正で既に実現済み**。Go の `deepValueEqual` も「動的型一致 → 値の再帰」の2段で、現在の deepEql は lockstep peel（Named/deref 層の型一致）+ `deepTypeEq`（複合 arm での型一致）+ nilish arm の3層が先に走る構造になっており、実質フェーズ1は前倒しされている。形式的な3関数分割を別途やる価値は「読みやすさ」のみで、新しい正しさは生まれない。優先度: 低。やるなら `deepTypeEq` を entry で一度だけ行う形への集約が自然。
2. **format 書き換えの中間表現化 — 妥当。そして指摘された `%p` バグの修正そのものになった（#116 で実装済み）**。directive+operand index の IR（`dir{start,end,pos,verb,stars}`）→ sequential spec + rebuilt args、という構成がレビュー提案そのまま。`%[n]`・`*`・EXTRA の扱いを IR 上で考えられるようになったおかげで、shared slot を消せただけでなく暗黙 arg カウンタの不整合（`%[2]v %p` が誤 operand を変換し得た潜在バグ）も同時に潰れた。提案の方向は正しかったと結論できる。

## 6.4 レビュー第3ラウンド: 5件の検証と2件の修正

第3ラウンドは5件報告されたが、現スタックトップで再現を確認したところ **3件は既に直っていた**（レビューのベースが古い状態での検出と思われる）。残り2件のみ diverge。

- **既修正（再現せず）**: `len(a[i()])` の call 省略と shadow された `len`/`cap` の畳み込み（#108）、`DeepEqual([]int{1}, []int64{1})` / `(*int)(nil) vs (*string)(nil)`（#111/#114）。全て現トップで `go run` と一致することを確認。
- **入れ子呼出しの引数スクラッチ衝突（#118）** — `callArgs` の `$argN` カウンタが call site ごとに 0 始まりだったため、`foo(f(), bar(g()))` で `bar` 側の hoist が外側の `$arg0` を上書きし `(10,21)` が `(20,21)` に。カウンタを `compiler.tmpSeq` に昇格し関数全体で一意化。評価順二相化（#105）で入れた機構の、入れ子ケースの見落とし。
- **`default` の無条件選択（#119）** — switchStmt が default clause に skip-jump を出さず、ソース位置で即 body に落ちていた（`switch 1 {default:; case 1:}` → `default`）。非最終 default も他 clause と同じく次テストへの jump を出し、「全テスト不成立」の継続先を default body に patch。fallthrough の前後接続は従来通りソース順。

### リファクタリング提案の評価（第3ラウンド）

1. **一時変数生成の専用ヘルパー集約 — 妥当（中）**。今回の `$argN` 衝突は「採番スコープを呼び出し側が握る」構造が原因で、`c.tmpSeq` で回避したが、`$tag`・named result slots・funclit 名など compiler 内の合成名は同じ罠を持つ。`c.fresh("$arg")` 的な発番ヘルパーに集約すれば今後の衝突を構造的に防げる。ただし現状の衝突面は `$arg` だけなので、効果は予防的。
2. **VM・intrinsics 間の型同一性判定の共通化 — 妥当（中）**。`deepTypeEq`/`deepDefEq`/`deepTypSpelling`（intrinsics）と VM 側の assignability・interface switch strict 比較・comparable 判定は同じ「typedef の同一性」を別々に判定している。実際にずれが存在する: DeepEqual は spelling 比較で匿名型を区別するが、VM の `BinEqlIface` は `==`/`deepDefEq` 系で `[]int` vs `[]string` の要素型を見ない方向の判定になっている経路がある。`typedefIdentical(a, b)` のような単一 API に集約し、各判定が「構造的同一性のどの側面を見るか」を明示できると、指摘系の再発を防げる。`deepTypSpelling`（AST printing）が runtime 非依存のまま `runtime` パッケージ側へ移せるかが設計の肝 — `ast.Expr` と `*runtime.TypeDef` だけに依存するので移動自体は可能。

## 6.5 レビュー第4ラウンド: 2件の修正

第4ラウンドは2件とも現スタックトップで再現した（[#122](https://github.com/podhmo/minigo/pull/122)、[#123](https://github.com/podhmo/minigo/pull/123)）。リファクタリング提案なし。

- **len/cap fold が operand base の call を見逃す（#122）** — `hoistedArgCalls` の対象が `ix.Index` だけだったため、`len(f()[0])` で base の `f()` が fold に巻き込まれて消えた（Go は index を fold するのは operand 全体に call/受信が無いときだけ）。`ast.Inspect` ベースなので対象を index 式全体（`ix`）に広げただけで base 側の call も拾える。評価順二相化のガード範囲の見落とし。
- **終了ブロックのローカル型が残留（#123）** — `typeSpecs`/`typeDefs`/`ifaceTypes` が関数単位のフラット map で `popBlock` と連動せず、`{type T struct{Y}}` 終了後の `type U struct{T}` が死んだ T を埋め込んで `u.X` が trap。3つの検索を `isTypeDeclName` と同じ「live ブロックに binding があるか」基準に統一 — ついでに local var `T` が外側の `type T` を透過させる var-shadow 穴も塞いだ。

いずれも「メタデータの寿命 ≠ 名前 binding の寿命」「最適化ガードの対象範囲の切り方」という §3 の構造的な反省の再発型。fscope の型メタデータ系は block 連動に揃えたので、この系の個別指摘はここで打ち止めのはず。

## 6.6 レビュー第5ラウンド: 3件の修正

第5ラウンドは3件とも現スタックトップで再現した（[#124](https://github.com/podhmo/minigo/pull/124)、[#125](https://github.com/podhmo/minigo/pull/125)、[#126](https://github.com/podhmo/minigo/pull/126)）。

- **[P1] DeepEqual の循環参照でホスト死（#124）** — `m["self"]=m` の比較が無制限再帰で recover 不能の stack overflow（Go は `true`）。`devisit{a,b}` の seen-pairs を Map/Slice/Struct の各 arm に入れ、再訪ペアは coinductive に `true`。`av == bs` のポインタ同一性ショートカットも併設（Go は「構造が同じ循環」を真とみなすので参照一致性判定で十分）。
- **[P2] struct の型同一性（#125）** — フィールド名だけの比較だったため `struct{X int}` ≡ `struct{X any}` が `true`、さらに兄弟ブロックの同名 `type T` 同士も一致。struct arm を `deepTypeEq`（AST spelling 含む完全な型同一性）経由にし、named def は宣言オブジェクト同一のみ一致へ。匿名 struct は従来通り形状比較なので別リテラルサイト同士は同一型のまま。
- **[P2] 内側 type 宣言が外側のメタデータを上書き（#126）** — `typeDecls`/`typeSpecs`/`typeDefs`/`ifaceTypes` が関数単位フラット map だったため `{ type M struct{...} }` が外側 `type M map[int]int` のエントリを破壊し、ブロック終了後も誤った型で解決し続けた。4 map を `blocks` と同じ per-block slice に変え、`recordType` で宣言ブロックへ書く構造に — §6.5 で「打ち止め」と書いたが、#123 は lookup の liveness を直しただけで書き込み側は依然フラットだった。この case が本当の打ち止め。

### リファクタリング提案の評価（第5ラウンド）

- **ブロックの binding に slot・宣言種別・型情報をまとめる — 妥当（中）。#126 はその弱い版として実装した**。提案は `blocks[name]→slot`、`typeDecls`、`typeSpecs`、`typeDefs`、`ifaceTypes`、`declPos`、`ifaceVars` を `[]map[string]binding` の単一レコードへ統合する方向。今回は「既存の並列 map を同じ push/pop 寿命に揃える」形に留めた: 参照点7箇所の修正で済み、効果も等しい（全 map がブロックと同じ寿命を持つので、取り違え・残留・上書きの系は構造的に消えた）。統合版の追加利得は「1フィールド追加＝1箇所変更」の見通しだけで、新たな正しさは生まれない。ただし fscope は現在 8 本の並列スライスを push/pop で揃えており、将来フィールド追加時の同期漏れリスクは残る — 次にこの構造を触る変更（例: 別種のブロックスコープ情報の追加）が出た時点で `binding` レコード化を検討するのが適切なタイミング。

## 6.7 レビュー第6ラウンド: 代入ターゲットの live storage 解決

第6ラウンドは1件、現スタックトップで再現した（[#127](https://github.com/podhmo/minigo/pull/127)）。

- **[P2] フィールド代入先が古い構造体を掴む（#127）** — `refTarget` が RHS 評価前に base を**値**として確定していたため、`s.X = replace(&s)`（replace は `*s = S{X:1}` で s 全体を置換）が死んだ Struct に書き込み `s.X == 1`（Go: `2`）。

調査で Go の lvalue モデルを実測で確定した: **代入先の base は格納時に解決される live なストレージ参照チェーンで、key/添字だけが評価時スナップショット**。`p.X = reseat(&p,q)` は新 pointee に書き、`s[i] = f()` は新 slice に書き、`m[k] = f()` は新 map に書く。一方 `&s[0]`（slice の要素アドレス）は評価時の配列を pin する — `&` 経路と `=` 経路で非対称になる。

修正は3層: (a) `refTargetBase` — base がストレージ運搬形（Ident/Selector/Index/Star）なら `refTarget` で live ref を吐き、それ以外（call・`&x` 式）は従来通り `expr` の値評価。(b) `*p = v` 用に `DerefRef`（格納時に `Deref(ptr)` を解く遅延 ref）と `OpDerefRef` を新設 — ポインタ値をそのまま積むと pointee がスナップショットされるため。(c) compound assign（`x op= y`）を同じ ref パイプラインに統一 — こちらは「ref 評価 → rhs 評価 → 読み出し+op+store」の順で、Go が読み出しを RHS 評価時に行うことも実測で確認済み（`s.X += f()` で `10+5=15` ではなく `1+5=6`）。`OpFieldRef` の cell 正規化は `&` 経路（B=0）のみに限定し、store 経路（B=1）は生のストレージ cell を保持。

付随修正: パッケージ修飾ターゲット（`runtime.MemProfileRate = v`）は base がパッケージオブジェクト（ストレージではない）なので `isImportName` で `expr` へ振り分け、`OpDeref` 上の IndexRef は `v.index`（map 対応の完全経路）へ流す。

### リファクタリング提案の評価（第6ラウンド）

- **`refTarget` を「変数ストレージを保持するケース」と「評価時点の参照先を保持するケース」に分ける — 妥当。そして今回の修正がほぼそのままの形になった（高）**。`isStorageBase`/`refTargetBase` が提案の分岐そのもの: ストレージ運搬形（Ident・Selector・Index・Star・Paren unwrap）は ref、非ストレージ形（call・`&`式・型名）は値。実装して分かったのは、提案が暗に想定する二分では収まらない点が2つ — `*p` は「評価時の pointee」ではなく「p が指す場所」を格納時に解く第三のケース（`DerefRef`）で、Ident だけ import 名判定が必要（パッケージは値オブジェクト）。つまり分岐は「storage / value」の2値ではなく「storage / 評価時 pointee / value」の3値が正確なモデルで、提案の方向は正しいが粒度はもう一段細かい。複合代入も同じ構造に乗せられたので、「この種の不具合を防ぐ」という狙い自体は達成されたと評価できる。

## 6.8 実施ラウンド（round-7）: リファクタリング提案の実行

§6.2–§6.7 で「妥当」と評価した提案を stacked PR として実施した（[Stack #139](https://github.com/podhmo/minigo/pull/138)、[#128](https://github.com/podhmo/minigo/pull/128)–[#138](https://github.com/podhmo/minigo/pull/138)）。実施結果:

| 提案 | 状態 | PR |
|------|------|-----|
| `runtime.Map` insert/delete/clear メソッド集約 (§6.2-1) | 実施 | [#128](https://github.com/podhmo/minigo/pull/128) |
| compiler 型・名前解決の共通化 (§6.2-2) | 実施 | [#129](https://github.com/podhmo/minigo/pull/129)（binding レコード、§6.6 統合案も兼ねる）、[#131](https://github.com/podhmo/minigo/pull/131)（`resolveName` ラダー） |
| `RuntimeError` 生成ヘルパー (§6.2-3) | 実施 | [#136](https://github.com/podhmo/minigo/pull/136)（`runtime/errors.go` のコンストラクタ群） |
| `deepEql` フェーズ分割 (§6.3-1) | 省略 | 報告自身の評価どおり「新しい正しさは生まれない」 — lockstep peel + `TypIdenticalStrict` で実質実現済み |
| format 書き換え IR (§6.3-2) | 実施済み | #116（当時） |
| `c.fresh` 一時変数ヘルパー (§6.4-1) | 実施 | [#137](https://github.com/podhmo/minigo/pull/137) |
| 型同一性判定の共通化 (§6.4-2) | 実施 | [#135](https://github.com/podhmo/minigo/pull/135)（`runtime.TypIdentical`/`TypIdenticalStrict`） |
| fscope binding レコード (§6.6) | 実施 | [#129](https://github.com/podhmo/minigo/pull/129) |
| `refTarget` storage/value 分岐 (§6.7) | 実施済み | #127（当時） |
| Named/UConst タグ操作の統一入口 (§3) | 実施 | [#138](https://github.com/podhmo/minigo/pull/138)（`runtime.Tag`/`TagOf`/`Unwrap`） |

usecasefuzz 再実行: 0 DIFF / 1 ACCEPT / 4 TRAP（lim-http/toml/xml/yaml — 既知境界）— **リグレッションなし**。

### 計画外の意思決定

- **型同一性の統合が実害 diverge を露出（#135）**: `eqlValue` の動的型ゲートが Go の (type, value) ペア比較からずれていた — `any((*int)(nil)) == any((*string)(nil))` が true、`any([]int) == any([]string)` が uncomparable panic（Go は false）、`structDefsEq` が `Binds` を見ていなかった。「判定器を一本化する」作業が各 arm の前提ずれを可視化したため、リファクタと同じ PR でゲート自体も修正（回帰 pin: `text_pass_ifaceeqtypes`）。deepEql の struct arm は `deepTypeEq`→`TypIdentical` に移し、REPL の decl 再マテリアライズ（cache eviction 後）でも name+pkg で一致する強度を選んだ — object identity だと同一宣言の再構築を別型とみなしてしまう。
- **panic payload の3系統整理（#136）**: 41 サイトの Message を族分けすると、文字列 payload（`r.(error)` が効かない）・`runtime error:` 二重プレフィックス（2サイト、Error() が再付与するため）・Go 1.23 以前の range-yield 文言が混在していた。`PlainError`（Go の plainError 系 — error 型だが Error() にプレフィックスなし）を新設し、`Recover()` は error-typed payload 全般を GoValue 化する形に一般化（PanicNilError も通る）。意図しない変更は fixture（`text_pass_panicpayload`）で `r.(error)` の成否まで含めて pin した。
- **funclit の invented params は `ic.fresh`（#137）**: 子コンパイラ側の hoisted `$argN` は ic の seq から採番されるため、param 名も親ではなく ic の seq から採る — 同一 seq 空間に揃えないと子スコープ内で衝突しうる。
- **UConst はタグ統一の対象外（#138）**: 提案は Named/UConst を併記していたが、UConst は `constant.Value` を包む別形で、materialize 入口（`materializeConst`/`materializeConstErr`）は既に一本化済み。`Tag`/`Unwrap` は Named のみに限定した。
- **`eqlValue` の Function arm は panic 維持（#135）**: 異なる func 型同士の比較も Go では false だが、関数値が signature typedef を持たないため同一性ゲートを掛けられない — 既存の近似（無条件 panic）を残した。

## 6.9 実施ラウンド（round-8）: コーパス SILENT 掃討 — recover/Callers の unwind モデル

`$GOROOT/test` コーパス再スイープ（145 programs）の残り SILENT を潰した。Stack #144 の最上位に3本の修正 PR（[#149](https://github.com/podhmo/minigo/pull/149)–[#152](https://github.com/podhmo/minigo/pull/152)）を積んだ。

| 対象 | 根因 | PR |
|------|------|-----|
| `recover1.go`（4行の差分） | `recover()` が「defers を持つ任意フレーム」から panic を見ていた。Go の `gorecover` は recover 呼出と panic の間に**非 wrapper フレームがちょうど1つ**ある場合のみ成功とする | [#149](https://github.com/podhmo/minigo/pull/149) |
| `devirtualization_nil_panics.go`（panic 行番号 -1） | `runtime.Callers` が unwind 済みフレームをスナップショット末尾に並べていたため、`CallersFrames` の `for f,next:=Next(); next` 走査が panic フレームに到達しなかった。unwind 境界（`unwindDepth`）に挿入する Go のトレースバック順へ | [#150](https://github.com/podhmo/minigo/pull/150) |
| ハーネス artifact（seed 20261003 の SILENT） | `x[lo:CALL(...)]` 形で複数の panic 源が競合 — Go は非 call オペランドの評価順を規定していないため、先に panic する側は実装依存。minigo の panic テキストが go 自身が同プログラムで出力したものなら order-legal として Pass に再分類 | [#151](https://github.com/podhmo/minigo/pull/151) |
| `recover.go`（`panic: 5` が脱出） | 正常 return 後の drain 中に deferred call が panic した場合、unwind 先は**スタック上に残る owner フレーム** — `runOneDefer` が境界を `dpos`（owner のスロット）に立てていたため owner が2フレーム目に数えられ recover が nil を返した。境界は「呼び出した deferred call が占めるスロット」（= `len(v.frames)`、unwind drain 中は `dpos` と一致） | [#152](https://github.com/podhmo/minigo/pull/152) |

### 実施内容

- **Go セマンティクスの導出**: `gorecover`/`gopanic`/`recovery`（/usr/local/go/src/runtime/panic.go）と実測プローブで確定した規則 — (a) recover 合法条件は「recover から gopanic まで非 wrapper フレームちょうど1つ」、(b) `defer recover()` は0フレームで**絶対に回復しない**（`func(){defer recover(); panic(5)}()` は Go でも `panic: 5` で落ちる）、(c) panic が drain 途中で consume されると `recovery` は当該フレームの deferreturn に着地し、残り defers は外側 panic が見える文脈で走る（recover1 test6 が黙る理由）、(d) deferred call 内の panic は同じフレームの残り defers へリンクスキップで継続。
- **`DeferBuiltinRecover` の既存期待値が非 Go だった**: 「`defer recover()` が panic を飲む」という古い pin は実測で Go と矛盾すると確認し、ワーカー func 経由の正当な形（`defer func(){ defer recover() }()`）に差し替え + 伝播を assert する `DeferBuiltinRecoverPanic` を追加。
- **unwind 境界の2段階**: `unwindDepth`（panic の deferred-call 連鎖が根付くフレームスタック index）を導入。pop 済みフレームの unwind drain では `dpos`、正常 drain では deferred call の invoke index（owner がスタック上に居るため +1）。同じ `unwindDepth` が `runtime.Callers` の unwinding スプライス点にも使えた。

### 残りの状況

- コーパス SILENT は全て既知の境界クラスに帰着: GC/finalizer 系（closure/deferfin/finprofiled/gc2/mallocfin/stackobj/stackobj3/tinyfin/heapsampling/init1）、unsafe.Pointer（initialize → #40）、スループット HANG（copy/divmod/maplinear/winbatch/heapsampling — copy.go は 50s で正解確認済み）、gcgort（Go でもデッドロック）、linkmain_run（ツールチェーンの tmpdir ノイズ）。新規の潰せる残件はゼロ。
- TRAP backlog（次に実装すべき面）: `unsafe.Pointer`×13、`reflect.*`×7、`complit.go` の `cannot use [...]*T as [*ast.CallExpr]*T`、`map.go` の `index assign on *runtime.IndexRef`、`turing.go` の `index on *runtime.UConst`、`peano.go` の stack exhausted — 全て main と同一（回帰なし）。
- usecasefuzz 再実行: 33 PASS / 0 DIFF / 1 ACCEPT / 4 TRAP（lim-http/toml/xml/yaml — 既知境界）— **リグレッションなし**。seed-20261003 ガード再実行: SILENT 1→0。

### 不備の振り返り

- **`unwinding` リストの用途発見が後出し**: Callers の unwind-order は recover 用 `unwindDepth` と同じ境界を再利用できたが、初版はリスト末尾への append で「最後尾=スキップ」という見えにくい欠陥を持っていた。frame/frames の論理順序（deferred 連鎖 → unwinding → その下の live）を最初から1箇所のスプライス関数にしておけば Callers・Recover の双方で順序バグが入らなかった。
- **recover の距離カウントは「境界の定義」が本質だった**: PR #149 は `dpos`（unwound フレームの論理位置）で全件整合したが、recover.go の正常 drain（owner がスタック上に残る形）では同じ `dpos` が境界として使えず「呼び出しスロット」が要った。「panic が unwind する先のフレームがスタックに居るか」で boundary が ±1 変わる — `runOneDefer` が `len(v.frames)` を取る形にして両ケースを一意にした。
- **ハーネスの false positive は mask ではなく意味論で解いた**: 「両側 panic でメッセージ違い」を無条件に揉めば数字系の真バグを隠す。go が同プログラムの別プローブで同じ panic を出していれば「その panic は authentic」= order-legal と判定する相互参照方式にし、unique-to-minigo の panic は引き続き flag する。

### 計画外の記録と判断

- **corpus 外の新規作業ゼロ**: TODO 残件は全て境界クラスで、コーパス再スイープからも潰し対象の新規 SILENT は出なかった（recover.go のみ）。hunt の新 seed 補充は不要と判断 — gen は同シードガードで clean。
- **`defer recover()` のテスト期待値是正を同 PR に同梱**: 実装変更とテストデータ是正は1根因（one-frame 規則）として同一 PR にした — pin しないと片方だけ残る危険があった。

## 6.10 レビュー第7ラウンド: unwind bookkeeping の3件（1件は回帰）

[#153](https://github.com/podhmo/minigo/pull/153) への独立レビューが `#149`–`#152` の unwind モデルに3件の不具合を上げ、全て現スタックトップ（`4a15545`）で再現を確認して修正した（`go run` と minigo の双方で検証）。

| 指摘 | 根因 | PR |
|------|------|-----|
| **回帰**: mid-drain recover 後の deferred call が panic すると新 panic が飲まれる — `recovered: P` の後 `f returned` / `outer: <nil>` で E2 が消失。さらに `v.inflight` に残り、無関係な後続 `recover()` が死んだ panic を拾う（`h sees: E2`） | drain 終了時の outcome 取得は「元 panic `p != nil` の時だけ `v.inflight` を拾う」形だった。consume 遷移が `p = nil` にした後で later deferred が raise した panic は誰も拾わない | [#156](https://github.com/podhmo/minigo/pull/156) — 条件を `p != nil \|\| (inflight != saved && inflight != nil)` に統一 |
| `runtime.Callers` が consume 後に owner フレームを二重表示（live + stale unwound） | consume 遷移で `v.unwinding` の死んだ panic のエントリが残ったまま — Go は recovery 後に unwound フレームを**一切**出さない（実測: supersede された元 panic の残りも含めて消える） | [#157](https://github.com/podhmo/minigo/pull/157) — `unwinding` エントリを panic タグ付きにして、遷移で consume 側+frame 自身の panic のエントリを除去（outer の live unwind は保持） |
| 新 panic が mid-drain で supersede すると `Callers` の unwound 順が狂う（`f.func2` が `g,f` の後に埋まる） | `unwinding` が panic をまたいで append 順一本 — Go は**新しい gopanic の unwound フレームを先**に、古い unwind の残りをその後に出す | [#158](https://github.com/podhmo/minigo/pull/158) — タグでグループ化し「最後に pop があった panic」を先に出力 |

実測で確定した Go セマンティクス（panic.go の `gopanic`/`recovery` と挙動プローブ）:
- deferred call が panic した時点で元 panic は**死亡**（superseded）— 新 panic を recover しても元 panic は復活せず、関数は正常終了する（`f-d2 recover: P` → `f returned` → `outer: <nil>`）。
- panic が recover で consume されると、その unwind が pop したフレームは Callers から**全て**消える — supersede 歴の残りも含め、live フレームだけが残る。
- `unwinding` のリスト順 = 「panic 単位のグループ化、新しい panic を先」— 単一 append 順では supersede 時に新 panic の frame が古い unwind の残りに埋もれる。

回帰 pin: `text_value_deferpanicrecover`（propagate+stale inflight）、`text_value_defercallersclean`（consume 後の二重表示）、`text_value_defercallerssuper`（panic グループ順）。

### リファクタリング提案の評価（第7ラウンド）

| 提案 | 判定 | 対応 |
|------|------|------|
| `frame.deferred` は write-only | 正しい — `sentinel` が marker の役割を担う | 削除（#159 内の cleanup コミット） |
| `invokeDeferred` の doc が「`defer recover()` が unwind 中の panic を拾う」と旧仕様のまま | 正しい — one-frame 規則と矛盾 | doc 書き換え（sentinel = wrapper slot、0フレーム → nil / 1フレーム → recover） |
| `imag` が TypeDef を inline 構築 | 妥当 | `real` も同形だったので両方 `runtime.BasicTypedef("float32")` に |
| `basicTypedefs` の lazy map は `builtins()` pre-fill 頼み | 妥当 — goroutine-safe の根拠が暗黙 | init 時 eager 構築に変更、`BasicTypedef` は map lookup のみに |
| `constNative` が `uconstNative` エラーを飲むのは `intOf`（panic）と非対称 | 妥当 — builtin 引数位置では materialize できない定数はプログラムの失敗 | `constNative` も `RuntimeError` payload で panic する形に統一 |
| `intOf` の panic payload が string（Go は error） | 妥当 — `recover()` で `r.(error)` が効かない | 4箇所の `uconstNative` 失敗点を `&runtime.RuntimeError{Msg:...}` に |
| `v.pcSites` が Callers のたびに無限増殖 | 正しい — ただし pre-existing・低頻度 | 本ラウンドは見送り（残課題; snapshot 単位なので実害は長寿命 REPL に限られる） |
| `bytesSliceOf` の nil が `[]interface {}` 綴り | 妥当 — `%T` が `[][]uint8` になるよう修正 | `anonSliceTyp("[]uint8")` で Typ を保持 |

### 不備の振り返り（第7ラウンド）

- **`inflight` の pickup が「`p != nil` 前提」で設計されていた**: consume 遷移が `p = nil` にする経路を作った時点で、後置ブロックの取得条件を見直すべきだった — 「`p` は遷移後も panic の残存を意味するか」という不変条件の検証漏れで、本スタック自身が回帰を入れた形。
- **`unwinding` のライフサイクル管理が「全部消す/残す」の二値だった**: 初版（#150）は `v.unwinding = nil` で全部消していたため outer unwind のエントリまで消せず、残す方向に倒したら consume 後の stale が出た。panic タグ付きにして「死んだ panic のエントリだけ落とす」が正しい粒度だった。
- **レビューの repro は最初から全件現トップで再現した**: 前ラウンド（3/5・5/7 が既修正）と違い、今回は 3/3 が真の未修正だった — unwind bookkeeping は相互に絡むため「直したつもりの組合せケース」がまだ抜けていた。
- **#151 は修正 PR ではなくハーネス PR**: 根因は「ジェネレータが実装依存の出力を生成する」側なので、minigo 側の挙動は変えていない（評価順の厳密 LTR は合法）。

## 6.11 実施ラウンド（round-9）: reflect TODO 掃討 + difffuzz 供給フェーズ

TODO.md の reflect 系未完了項目を 1 root cause = 1 PR のスタックで潰し、その後 difffuzz `-domain reflect` の hunt→triage→pin→fix を回した。さらにレビューで指摘されたバグ4件＋重複/リファクタ5件を子セッションに要/不要判断させ、要のものを同じスタックへ継続積みした。[Stack #201](https://github.com/podhmo/minigo/pull/199)（[#199](https://github.com/podhmo/minigo/pull/199)–[#275](https://github.com/podhmo/minigo/pull/275)、69 PRs、main 直積み）。

### 実施内容

| フェーズ | 内容 | PR |
|------|------|-----|
| TODO 5件 | `fmt` が facade `reflect.Value` を1段 unwrap、`reflect.TypeAssert[T]` バインド（lim-xml ブロッカー解消）、`Value.SetCap` バインド、index-panic 文言修正、shared-global 破壊経由の発散2件 | [#199](https://github.com/podhmo/minigo/pull/199)–[#205](https://github.com/podhmo/minigo/pull/205) |
| 供給フェーズ前半 | `Type.In/Out` bounds、`OverflowInt/Uint` 符号跨ぎ、`Bytes` の kind ディスパッチ、`Method(i).Func`、`StructField.Offset`（amd64 layout）、`Slice`/`Convert` の ref・ro 伝播、`Type.Name` alias 畳み込み、`ValueOf` on facade、`FieldByIndex` embedded ptr、`Append*`/`Copy` 要素型等 | [#229](https://github.com/podhmo/minigo/pull/229)–[#250](https://github.com/podhmo/minigo/pull/250) |
| generator 拡張 | (a) `Runner.mask` が `0x[0-9a-fA-F]{6,}` → `0x…` を畳む（アドレス揺らぎで PASS↔SILENT が反転するのを止める）、(b) kind タグ付き 78 seeds＋合成 ctor（`SliceOf`/`MapOf`/`PtrTo`/`ChanOf`/`ArrayOf`/`New`/`MakeSlice`/`MakeMap`）、(c) `Grow`/`Slice3`/`FieldByIndexErr`/`FieldByNameFunc`/`SetZero`/`Complex`/`Pointer`/`UnsafePointer`/`UnsafeAddr`/`Recv` の probe 群 | [#220](https://github.com/podhmo/minigo/pull/220), [#222](https://github.com/podhmo/minigo/pull/222), [#242](https://github.com/podhmo/minigo/pull/242), [#251](https://github.com/podhmo/minigo/pull/251) |
| 供給フェーズ後半 | `kindStr` の `on zero Value`、`Field`/`NumField`/`FieldByName` が host ptr を deref しない、`CallSlice` variadic→exact arity→`[]Elem` 代入性、`Call` arity が vc チェックに先行、`callSig` は typedef シグネチャ（receiver 込み）優先、`TypeAssert` が host `flagRO` を見る、`Pointer`/`UnsafePointer`/`UnsafeAddr` 実装、path 修飾 selector の `resolveTypeRef` 解決、`resolveExpr` が `*T` を保持、`Value.Grow` バインド | [#243](https://github.com/podhmo/minigo/pull/243)–[#263](https://github.com/podhmo/minigo/pull/263) |
| レビュー駆動 バグ修正 | 全4件 NEED 判定（子セッションが oracle probe で実測検証）: (a) `Grow` cap≥256 での第二無限ループ — `(newcap+768)/4` 代入で縮小、`nextslicecap`/`roundupsize` を go1.26 実装（sizeclass・malloc header・noscan 分岐）まで忠実ミラー、29ケース一致; (b) `Set` のソース側 `ro` チェック欠落（SILENT — host 側分岐も同じ欠落を発見・同修正）; (c) `ValueOf` on タグ付き host box で `CanSet=true`＋Set がライブ状態を書き換え → copy semantics 復元（複合的根因で3PR: `(*p).Set` 型の pointee 書込み、`typSpelling` の二重修飾 `bytes.bytes.Buffer` による複合 td 別キー化）; (d) `FieldByName` が昇格フィールドの Offset を加算 → Go 通りローカル値 | [#265](https://github.com/podhmo/minigo/pull/265)–[#270](https://github.com/podhmo/minigo/pull/270) |
| レビュー駆動 重複/リファクタ | `runtime.DisplayName` 新設で表示名ロジック4実装を集約（`typeName`/`msgTypeName`/`typedefSpelling`/`TypGoSpelling` — 指摘の表記ブレは再現せずも別の本物を発見: `map[byte]int`、alias import 修飾、無名 struct が `struct{}` に潰れる）、`numParams`/`sliceView`/`hostMethodSet` 抽出、`RValue.Unwrap`→`Payload` 改名、nil `.V` ガード | [#271](https://github.com/podhmo/minigo/pull/271)–[#275](https://github.com/podhmo/minigo/pull/275) |

全 fix は `testdata/difffuzz/<slug>/` に seed pin（PENDING なし → 即必須テスト昇格）。

### 残りの状況

- TODO.md の reflect 系 `[ ]` は全て `[x]`。
- difffuzz yield は減衰: 現カバレッジで ~1/15–20 seeds。generator 拡張（#251）で新 probe 面が開き、即座に pointer-accessor・selector-path・callslice-gates・star-param の4件を供給した。
- 未バインド op（`no member` trap → 実害ありの backlog）: `Slice3`/`FieldByIndexErr`/`FieldByNameFunc`/`SetZero`/`CanConvert`。probe 済みなので hunt が回れば発散として浮く。
- レビュー全項目を処理済み: バグ4件は全て NEED、重複/リファクタ5件は 4件採用＋1件部分採用（`sliceView` — 重複は実際は Grow+Bytes のみ、SetLen/SetCap は非 slice Named を trap する意図的形状）。子セッションが「やらない」と判断した残置: `anonTag`（identity 側）、`tdName` 系（内部 diagnostics 用）、無効パッケージ限定の乖離（oracle が走らず seed 化不可）。
- 残存する facade 制約: vc なし `Func` Value（`Type.Method(i).Func`）は正しい arity でも `needs a caller context`（VMCaller を facade 側で作れない構造制約。arity gate は先行するので fuzz が拾う panic 文言は正しい）。
- pin 不可 artifact（記録のみ）: (a) `MapKeys()` 順は Go 自身がランダム化 — map 順に依存する発散は pin しない、(b) `reflect.Value` 内部 `flag` バイトを直接読む合成プローブ（実害なし）。
- `usecasefuzz` 再実行: 41 PASS / 1 ACCEPT（inspectuse）/ 1 REJECT（lim-cgo — cgo 既知境界）/ 2 TRAP（lim-http, lim-xml — 既知境界）。`lim-yaml` は TRAP↔DIFF↔PASS の揺らぎ（map 順 artifact と思われる — 単独再実行では PASS）。`lim-toml` は TRAP から PASS に改善。**リグレッションなし**。

### 不備の振り返り

- **スタックブランチへの誤コミットが3回**: `git branch --show-current` を commit 前に確認せず、同じファイルを触る PR 間で hop して混入した。`git reset --hard`+`push -f`→cherry-pick と file 単位の patch 分割（`git diff` → `@@` 単位で `git apply` 分け）で回復したが、確認はコストゼロなので常時行うべきだった。
- **generator の 'x'+Wrap emit バグ（#251 内で自爆）**: `rchain.body()` の 'x' 分岐が Wrap を honor せず `v1rGrowS` を生成 — `TestGeneratedProgramsCompile` が `undefined: v1rGrowS` で捕捉。generator を拡張するときは「emitted プログラムが go でコンパイルされる」までを1ケースとして回すべきだった。
- **`Grow` の `newcap=0` 無限ループ**: growslice 近似ループで `0 *= 2` が停止しない — 実害テスト（nil slice への Grow）で初めて出た。uint loop の termination 条件は初期値 0 の corner を常に疑う。
- **detached-cell の Named 喪失（#248 の根因）**: `*runtime.Cell` が裸の underlying を保持し `Deref` が `Named` を剥がすため、cell 経由の `get()` は宣言型タグを失う — `MethodByName` の member 解決は `td` 駆動の再タグ retry が要った。「cell 越しの値」は identity 層が一個外れる、という不変条件の見落とし。
- **host `flagRO` の居所（#250 の根因）**: facade `v.ro` は script-domain のみ — host 値の read-only は `!v.rv.CanInterface()` に居る。`Set`/`Interface` は host-op 先行の自己呼びで拾えていたが、`TypeAssert` は facade 側ゲートだったため `rv` を見る必要があった — 「gate をどちら側で置くか」の一貫したポリシー（host arm は host op 自身の panic を先に発火させる、facade arm は facade 側値の flag を見る）を最初に立てるべきだった。レビューで判明した `Set` のソース側欠落（#266）も同じ居所問題 — dst だけ見て src を見ていなかった。
- **`Grow` の第二ループバグがレビューまで残った（#263 → #265）**: 上記 `newcap=0` を直したとき、同じループの `(newcap+768)/4` が加算ではなく代入であることを見落とし、cap≥256 で縮小→無限ループのハングを本スタックに残した（本レポート自身が「newcap=0 側を潰した」と書いている間に ≥256 側が生存していた）。実害テストは `cap0 → need` 起点しか書いておらず、成長経路の別区間を probe していなかった — corner を1個潰してもループ全体の Go 対応表（cap→cap）を検証するまで「近似ループの正当化」は終わっていない。最終的に `nextslicecap`/`roundupsize` の sizeclass 丸め込み忠実ミラーに置き換えた（近似でなく写経、が正解だった）。
- **レビューが「自分で追加したコード」の退化を掴んだ（#210 系 → #267–#269）**: host 値への typedef タグ付け（`Named{td, GoValue{*T}}` box）はそのラウンドで正しかったが、`ValueOf` 経路が box をそのまま返して copy semantics を壊し、さらに `Set` が cell 内の box 形状を剥がす・`typSpelling` が selector 修飾子を型名として再修飾する、という3つの連鎖根因を生んでいた。追加した機構の「値が何経路で流れるか」の網羅確認が不足していた — box 形状は `get()`/`set()`/deref/ValueOf/Set の全経路で不変条件として検証すべきだった。

### 計画外の記録と判断

- **harness 側の修正が混ざった (#220, #222, #242, #251)**: 「発散」ではなく「generator が拾える形にする」PR として別積み。発散供給が枯れたら generator 面を広げるのが次の正規手段 — yield の天井を上げる投資として 4 本は妥当だった。
- **dispatch.go の engine 側修正が1件だけ混じった (#255)**: `resolveTypeRef` の SelectorExpr が path 修飾を unknown import にしていた — minireflect ではなく engine の型解決。reflect 系の外側に見えるが、発散の根因は exprOf の PATH 修飾 AST が解決経路に流れ込むことなので 1 root cause として残した。
- **`reflect_grow` は emitted 形式の手書き seed**: 修正後の build では emit がこの発散をもはや生成できないため、emitted 形状（try() ラッパ + panic 文言プリント）の最小プローブを手で pin。pin の目的は回帰防止であり provenance 純度ではない、という判断。
- **hunt の打ち切り判断**: yield ~1/15–20 seeds に逓減し、残件は `no member` 系 backlog + pin 不可 artifact に集約 — 追加 hunt より binding 実装の方が価値が高い局面に入ったところで打ち切り依頼。進行中の `Grow` だけ仕上げて停止。
- **レビュー駆動フェーズは子セッションへ委譲**: ユーザー指示により、バグ4件→リファクタ5件の順で「各項目を子が oracle probe で要/不要判断→要のものを重要度順に 1 根因 1 PR で同スタック継続積み」。子は bug3 が複合的根因であることを検証中に自力で2件の別根因（cell box 剥がし・typSpelling 二重修飾）を発見し 3PR に分割、レビュー指摘の表記ブレ主張は再現しないことを実測で否定しつつ別の本物のブレを掴んだ — 「レビュー文面の検証」が「レビュー趣旨の回収」に昇格した好例。リファクタ項目は純粋な整理は seed なし、挙動変化（DisplayName 集約に伴う表記修正）のみ seed pin という線引きを適用。

## 6.12 実施ラウンド（round-10）: Stack #333 — difffuzz 掃討・corpus sweep・連鎖 rebase・レビュー対応

本セッションの全体像。発端は TODO.md の difffuzz 系未完了項目を「1 root cause = 1 PR」で stacked PR に積む指示（上限 30 PR、枯渇時点で終了、枯れたら `gen` hunt で補充）。成果: **Stack #333 に 27 PR（#331–#359）を構築し、続けて連鎖 rebase＋別エージェントのレビュー7件対応＋本レポートで計 35 PR**。queued の全 difffuzz 項目を潰し、追加で reflect TRAP バケット・API 面監査・`$GOROOT/test` コーパス再スイープ×2を流した。

### 実施内容

| フェーズ | 内容 | PR |
|------|------|-----|
| TODO difffuzz 残件 | reflect backlog の掃討 — `Method.Func` のメソッド式＋caller 接続、`Append`/`AppendSlice`/`Copy` の nil/named 正規化、`Value.Slice3`、`Value.SetZero`、`Value.FieldByIndexErr`、`FieldByNameFunc`（Go の annihilation まで忠実移植）、`Can*` 系一式（CanConvert 含む）。併せて手書きシナリオ用の `casefuzz` skill を追加 | [#331](https://github.com/podhmo/minigo/pull/331)–[#339](https://github.com/podhmo/minigo/pull/339)（#332 は casefuzz skill） |
| API 面監査 | 185 件の reflect TRAP 消化後に残差を棚卸し — `Value.Clear`（kind ゲートのみ・settable 非必須の Go 仕様）、`Value.Seq`/`Seq2`＋`Type.CanSeq`/`CanSeq2`（yield を GoValue 箱化して range 変数のメソッド解決を通す）、`Type.Fields`/`Methods`/`Ins`/`Outs` 反復子、fmt の `&[..]`/`&map[..]` ポインタ描画（Go printPtr: トップレベルのみ下降）、range-over-func の over-yield 許容、`Value.Fields`/`Methods` | [#340](https://github.com/podhmo/minigo/pull/340)–[#345](https://github.com/podhmo/minigo/pull/345) |
| nil レシーバ・埋め込み解決 | nil `*T` ホストレシーバへのメソッド dispatch、promoted pointer method の nil 埋め込み `*T` 束縛、promoted method の BFS shallowest-wins（後述 — #348 に包含）、nil map の MapIter、capture-free func literal の共有（Go static funcs 相当）、map base の `IndexRef` 解決（write-through — レビューで回帰が見つかった箇所）、evaluated array length の typedef AST fold、GoValue box 上の named 型メソッド集合 | [#346](https://github.com/podhmo/minigo/pull/346)–[#354](https://github.com/podhmo/minigo/pull/354) |
| corpus sweep 第 2 ラウンド | `reflect.MakeFunc` 実装（callback は plain callable、生成 func は typedef 付き BuiltinFunc+RValue — `recover.go` を byte-identical PASS 化）、`ReadMemStats` を安定スナップショット化（`closure.go`/`gc2.go` — ホストアロケータのカウンタが script の delta 判定を誤爆させていた）、keyed array/slice literal 要素の `v.coerce`（`initialize.go` — `UConst` 残り vs `Named{byte}` で DeepEqual が false になっていた）、`typeMatches` の `*runtime.GoValue` case（`gcgort.go` — `interface {} is complex64, not complex64` の trap が host WaitGroup で masked され「本物の deadlock」と誤分類されていた）＋ TODO 記録 | [#355](https://github.com/podhmo/minigo/pull/355)–[#359](https://github.com/podhmo/minigo/pull/359) |
| 連鎖 rebase | main が +1 コミット進んだ（#348 promoted-method BFS が別経路でマージ）ため、27 ブランチを `git rebase --onto <new prev> <old prev> <branch>` で底から順に巻き上げ。conflict は #347/#349 系のみ（#348 と同一領域）。#349 の実装部分は main の `promotedMember`（BFS + shallowest-wins + ambiguity + nil-path）に完全包含されたため pin のみに縮退、タイトル/本文を「difffuzz: pin the shallowest-wins promoted-method case」に差し替え（pin は main の実装でも PASS 確認済み） | #349（内容縮退） |
| レビュー バグ ①（回帰） | map 要素への lvalue write-through が型を問わず効いていた — `ma["a"][1]=9`（map[string][3]int）や `mp["a"].X=9`（map[string]struct）が Go では compile error のところ格納オブジェクトを黙って書き換える（#352 の IndexRef write-through の TRAP→SILENT 転化）。`runtime.SharedElem` で参照形要素（slice/map/chan/func/pointer 系）のみ write-through に絞り、array/struct/scalar 要素は従来通り trap。`m[k]=v`/`+=`/`++` の全体代入は不変 | [#364](https://github.com/podhmo/minigo/pull/364)（pin: `mapref_elemref` + `TestMapElemLvalueTraps`） |
| レビュー バグ ② | `reflect.MakeFunc` がコールバックの**出力**側を一切検査していなかった — `func() (int,int)` シグネチャに 1 値しか返すコールバックで Go は panic するが黙って `len(outs)==1`。呼び出し時に Go と同じ panic 文言で wrong-return-count / zero-Value / not-assignable を検査（assignability≠convertibility — `int32`→`int64` は panic、実測で確認） | [#365](https://github.com/podhmo/minigo/pull/365)（pin: `reflect_makefunc-outs`） |
| レビュー 欠落 ③（判断: 要） | `emitLenFolds` が汎用実装なのに `*ast.ArrayType` トップレベルでしか呼ばれず、ネストした型式の非定数 array length が未 fold。probe すると `*[N]`/`func`/`struct`/`interface` は lazy package-scope path で既に動いており、実質の穴は `map[K][N]V` と `chan[N]T` のみだった → 全 typeExpr case から一様に呼ぶ形に拡張（Go の評価順と byte-identical を確認） | [#366](https://github.com/podhmo/minigo/pull/366)（pin: `arraylen_nestfold`） |
| レビュー リファクタ ① | 「3 か所並存の埋め込み BFS」は rebase 後に縮小済み（`findMethod`/`hostFieldName` は #348 で削除）— 残る `promotedMember`（値レベル）vs `RType.FieldByNameFunc`（型レベルの annihilation 移植）は別アルゴリズムで統合対象外。実質的重複は `FieldRef.Get`/`Set` の同一 11 行ループのみ → `FieldRef.find()` に共通化（挙動不変） | [#367](https://github.com/podhmo/minigo/pull/367) |
| レビュー リファクタ ② | `FieldByIndex`（panic）/`FieldByIndexErr`（error）の nil-ptr 差分だけを hook に分離して一本化（`fieldByIndexWalk`） | [#368](https://github.com/podhmo/minigo/pull/368) |
| レビュー リファクタ ③ | `IndexRef.sliceOf`/`mapOf` の Named+Deref 同一ループを `IndexRef.container()` に統合 | [#369](https://github.com/podhmo/minigo/pull/369) |
| レビュー リファクタ ④ | `foldNextArrLen`(vm) と `emitLenFolds`(compile) の平行 DFS — assert 追加ではなく共有化を選択。`runtime.ArrayLenNodes` が走査順の単一 source を提供し、compiler は emit、VM は nodes[0] を fold — 両側の平行実装約 80 行を解消 | [#370](https://github.com/podhmo/minigo/pull/370) |
| 本レポート | この round-10 セクションの追記 | #371 |

### 残りの状況

- difffuzz 系キューは枯渇して終了（掃討フェーズ 27/30 PR、上限未到達）。gen hunt は text/num/reflect 全ドメイン・depth 6 まで飽和（新規 SILENT 0）。レビュー指摘は全件処理済み（バグ2件＋欠落1件＋リファクタ4件、不要判定なし）。
- 残件は全て境界クラス: GC-finalizer 系 6（`SetFinalizer` は no-op 設計）、`unsafe.Pointer`×13＋`unsafe.String`/`Offsetof`/`FuncForPC`（#40 ポインタモデル・ホスト PC 境界）、`peano.go` フレーム上限、`linkmain_run.go` tmpdir 非決定、HANG×6 は main でも再現するスループット限界。
- レビュー/ probe で見つかった新規ギャップは stack 先端の TODO.md に記録: **`map[string]*[3]int` の内部書き込みが依然 trap**、**struct 要素の field write が一律 trap** — 次ラウンドの入口。
- Stack #333 は計 35 PR。CI は rebase 後の先端および各追加 PR で緑（head が全祖先を含むため累積検証）。リファクタ4件は全て挙動不変 — difffuzz pins は全緑のまま。

### 不備の振り返り

- **write-through の適用範囲に「参照形」という不変条件を書いていなかった（#352 → #364）**: IndexRef の write-through を入れたとき「map 要素が書き戻せるか」を kind 無しに開けたため、Go の compile error に相当するケース（array/struct 要素の部分書き込み）まで静かに通した。格納コピー vs live 参照の区別は §6.7（#127）で一度構造化した系で、同じ鏡をもう一度踏んだ形。
- **コールバック境界の検査が入力側だけだった（#355 → #365）**: MakeFunc 実装時に `checkCallArgs` を入力（呼び出し引数）にのみ適用し、コールバックの戻り値側（arity・assignability）に同型のゲートを置かなかった。MakeFunc は 1 根因に3つの層症状（callable≠reflect.Value・`return nil`=TypedNil slice・bare 引数の typedef 欠如）をひとつの bridging 修正で閉じていたが、「ホスト⇄script の両方向でシグネチャ制約が効くか」の確認が out 側に及んでいなかった。
- **ホスト側の共有リソースが script の観測値を汚す**: `ReadMemStats` が `goruntime.ReadMemStats` を素通ししていたため interpreter 自身の allocation が script の delta assert（`n0 != m.Mallocs`）を GC タイミングで不定に誤爆させた。「ホストカウンタは script には見せない」を明示しないと、同一クラス（runtime.GOMAXPROCS・NumGoroutine 等）で再発しうる。
- **コピー忘れの要素経路**: keyed literal は positional 側が既に coerce していたのに要素格納で素通し — 「literal 要素は全経路で coerce する」不変条件が kv 分岐に書かれていなかった。deepEql の Named-peel strictness（`aNamed != bNamed → false`）がこの形状差を検出した — 型タグの厳密化がかえって別バグを晒した構造。
- **panic が host 呼び出し内部で飲まれる観測性ギャップ**: goroutine panic → `proc.fail` → 後続 spawn は未実行 Task 化 → host WaitGroup のカウントが下りず、root が `WaitGroup.Wait` 内で blocked だと真の panic が表示されず deadlock に見える。「`fatal error: all goroutines are asleep` = 別 goroutine が既に trapped」と見抜く bisect 手順（worker body を逐次実行して真の panic を露出）を TODO.md＋メモリに記録。構造修正（abortable host call）は未着手。
- **`typeMatches` の GoValue 網羅漏れ**: host box 値（complex64 — script 複素型が存在しない、bytes.Buffer）が全 concrete assert で `interface {} is complex64, not complex64`。KindPointer の GoValue 分岐と同じ native-type 比較を top-level にも置く見落とし — assert 判定器の分岐表に「host box」列がなかった。
- **汎用機構を置いても配線が一箇所止まり（#353 → #366）**: `emitLenFolds` は汎用に書いたのに呼び出しが `*ast.ArrayType` のみ — 「機構が対象となる AST 形すべてから呼ばれるか」は配線の網羅確認が要る。probe 後は実質穴が map/chan のみと分かったが、一様呼出し化で残差も含めて閉じた。
- **平行実装のドリフトは「共有 source」で解く方が正しい（④）**: `foldNextArrLen` と `emitLenFolds` は DFS 順序一致を暗黙に要求する平行 DFS — 順序 assert のテスト追加も選択肢だったが、子セッションは走査自体を `runtime.ArrayLenNodes` に共有化する方を選んだ。assert は「ずれたら教えてくれる」止まりで、共有化はずれる余地自体を消す — 後者が正しい判断。

### 計画外の記録と判断

- **#349 が rebase で pin のみに縮退**: stack 内の promoted-method BFS 実装が main 側の #348 に完全包含されていたため、rebase 適用後の diff は testdata pin のみに。実装を消し込んで pin と差し替えた PR タイトル/本文も追従更新 — 「stack 内の別 PR が main で別実装として着陸」した場合の自然な帰結。force-push による全ブランチ書き換えは破壊的操作だが、ユーザーの明示指示（「開始前にmainからrebaseしたほうが良いかも」）で実施。
- **レビューは rebase 前の差分に対するもの**: 指摘の半分は現行コードで部分的に陳腐化していた（findMethod 系の並存指摘、emitLenFolds の「まだ trap」範囲）。各項目を現スタック先端で再検証してから判断させる運用を子セッションにも継承 — §5 の「レビュー指摘は現スタックトップで再現を確認してから直す」と同じ教訓の再確認。
- **誤分類の訂正を TODO.md に記録**: `initialize.go`（以前「DeepEqual/unsafe.Pointer 境界」と注記）は実は keyed 要素の uncoerced 格納、`gcgort.go`（「本物の deadlock」）は masked trap — sweep 中に境界と分類していた項目が probe で真のバグと判明した分を訂正した。
- **deadlock masking は修正せず記録に留めた**: root が host call 内 blocked のとき panic を露出するには abortable な host 呼出し設計が要り、本ラウンドの粒度を超える。観測手順だけ確立して残置。
- **子セッションへの委譲**: バグ2件＋欠落判定1件＋リファクタ4件の計7件を、各項目ごとに oracle probe（`go run`）で要/不要を判断させて 1 根因=1 PR で積ませる形に委譲。リファクタ①の「3本BFS」は実際には縮小済みで実質重複のみ残部修正、④は提案外の共有化アプローチを採用 — 文面通りではなく趣旨に沿った判断を要求した結果として妥当。
- **新規ギャップの分離記録**: probe 中に見つかった `map[string]*[3]int` 内部書き込み・struct 要素 field write の残存 trap は「この stack の指摘項目」ではないため TODO.md への記録に留め、次ラウンドの入口とした。
- **30 PR 上限には達せず枯渇終了**: 掃討キューが先に尽きたため打ち切りルール（30到達）を発動せず終了 — §5 の再開 prompt がそのまま通用する状態に戻った。

## 6.13 実施ラウンド（round-11）: Stack #417 — difffuzz 掃討・バッチ幻影の正体・外部レビューの2段階委譲

本セッションの全体像。発端は前回同様 TODO.md の difffuzz 系未完了項目を「1 root cause = 1 PR」の stacked PR で順次潰す指示（上限 100 PR — 指示文は 30 とあったが途中で「100のつもりだった」と訂正。枯渇時点で終了、枯れたら `gen` hunt で補充）。成果: **Stack #417 に 22 PR（#415–#455）を構築**。lang domain の hunt 補給から始め、SILENT/TRAP バケットを掘り進め、繰り返し出ていた「バッチ限定の幻影」の正体（panic 状態リーク）まで辿り着いた。終盤に届いた外部エージェントのレビュー（バグ2件＋リファクタ候補3件）はユーザーの指示通り「要不要判定付き」で子セッション2件に2段階委譲し、全件処理させた。

### 実施内容

| フェーズ | 内容 | PR |
|------|------|-----|
| hunt 補給 + harness 修正 | lang domain 初回 hunt の発散を6件 PENDING pin として取り込み（`sliceelem_ptrmeth`/`methodset_ptrrecv`/`binop_evalorder`/`assert_typename`/`instant_tname`/`spread_nilinfer`）。併せて oracle が未到達の probe を gen 側で skip し emit 済み case を standalone 検証する harness 修正 | [#415](https://github.com/podhmo/minigo/pull/415)–[#416](https://github.com/podhmo/minigo/pull/416) |
| SILENT 掃討 | index 経由の要素レシーバへ pointer method を bind（`sliceelem_ptrmeth`）、Go の receiver 規則を method set 判定と host interface probe に適用（`methodset_ptrrecv`）、純粋二項オペランドの評価を演算時点に遅延（`binop_evalorder`）、conversion panic の動的型綴りを Go の名づけ方に（`assert_typename`）、instantiation 型引数の DisplayName（`instant_tname`）、spread call の型引数を slice 要素 typedef から推論（`spread_nilinfer`） | [#418](https://github.com/podhmo/minigo/pull/418)–[#423](https://github.com/podhmo/minigo/pull/423) |
| goroutine panic のプロセス意味論 | goroutine panic が fatal と決まった時点で proc を fail させるタイミング修正 + blocking host call 内で park した goroutine をプロセス死時に解放（goroutine 経由の spread 呼出しが hang していたのが正体）+ 回帰 pin（`spread_nilinfer_goroutine`） | [#424](https://github.com/podhmo/minigo/pull/424)–[#426](https://github.com/podhmo/minigo/pull/426) |
| assert/deref の静的型 | assert オペランドの静的型を call/conversion 形に引き継ぐ（`assert_exprstatic`）、`starOperandIsValue` で bare name 以外の deref オペランドを値扱い（`mapkey_deref`） | [#427](https://github.com/podhmo/minigo/pull/427)–[#428](https://github.com/podhmo/minigo/pull/428) |
| generic 推論の静的 bind | TRAP バケットの2系統（`undefined: T`×15、`cannot use interface value as *T`×9）の根因 — 型引数を値の動的型で unify していた。引数ごとの宣言 typedef をコンパイル時に積む `argStatics` を新設し、`popArgs`→`inferBinds`→`unifyType` で静的型を優先（defer/go も同じ statics を運搬） | [#432](https://github.com/podhmo/minigo/pull/432)（pin: `infer_staticbind`） |
| バッチ幻影の正体（計画外の核心） | interface `==` の uncomparable panic — 同一 uncomparable 動的型なら nil 有無に関わらず panic（`ifaceeq_uncomparable`）。deferred call 内 panic の `inflight` リーク — 正常 drain が saved 状態を復元せず、消化済み panic が無関係な後続 `recover()` に届く（`deferpanic_stalerecover`）。後者が「standalone では発散しない」WARN の正体 | [#434](https://github.com/podhmo/minigo/pull/434), [#436](https://github.com/podhmo/minigo/pull/436) |
| 外部レビュー phase 1（子委譲・バグ） | `sync.Once.Do` 内 panic が呼出し側 recover に届かない → helper goroutine を `proc.syncCallers` に登録し同期コールバック専用の `syncCall` 子 VM に reroute、panic を join 経由で呼出し panic に変換（`synconce_panicrecover`）。埋め込み `struct{ A; *T }` で `A.T` の値訪問が共有 `seen` を消費し直接 `*T` の pointer receiver を隠す → cycle 検出を per-path 化（`methodset_embedpath`、methodSetOfU/methodFuncs 双子両方） | [#448](https://github.com/podhmo/minigo/pull/448), [#449](https://github.com/podhmo/minigo/pull/449) |
| 外部レビュー phase 2（子委譲・リファクタ） | `ifaceMember`≡`ifaceOffer` → `runtime.IfaceMember`（6 call site 挙動不変）。`methodSetOfU`+`methodFuncs` → `methodWalkU` 一本化 — **drift 実在**: methodFuncs が AST 無し facade interface 埋め込み（`struct{ fmt.Stringer }`）を見落としていた。`funcTypeName`+`funcSigSpelling` → `runtime.FuncGoSpelling`（Pkg/File/Binds ctx 引き継ぎ、`TypGoSpelling` で bound 型引数を clause 名で修飾） | [#452](https://github.com/podhmo/minigo/pull/452)–[#454](https://github.com/podhmo/minigo/pull/454) |
| レビュー検証中の新規発見 | `x.(func(int) int)` が `func(Point) Point` に true — assert switch が匿名 func typedef を kind のみで受理。修正は本ラウンドの粒度を超えるため TODO.md 記録に留めた | [#455](https://github.com/podhmo/minigo/pull/455) |
| 本レポート | この round-11 セクションの追記 | 本 PR |

### 残りの状況

- difffuzz 系キューは枯渇して終了（22/100 PR、打ち切り未発動）。gen hunt は lang seeds 202610059–066 連続クリーン（前回 3 SILENT + 24 TRAP だった 059 の再実行を含み、#432 系修正の実効を確認）、num/text/reflect 各 seed 全 PASS。testdata/difffuzz に PENDING 残りなし（全て必須テストに昇格）。
- TODO.md の difffuzz 節の open 残件は2件: `$GOROOT/test` corpus sweep（境界クラスのみ）と #455 で記録した func-signature assert gap — 次ラウンドの入口。
- Stack #417 は 22 PR 全て OPEN・MERGEABLE・checks SUCCESS。
- 外部レビューは全件処理: バグ2件はどちらも真（stack tip で byte-for-byte 再現確認後に委譲）、リファクタ3件は全て「要」判定で実施 + 副産物の新規ギャップ記録1件。

### 不備の振り返り

- **panic bookkeeping の対称性を `r!=nil` 経路にしか書いていなかった（#436）**: `runOneDefer` が deferred call の panic を `v.inflight` にインストールする経路を入れたとき、正常 drain（`r==nil`）終端で `p = v.inflight` として取り出すだけで saved 状態を復元していなかった。結果、包囲する unwind がその panic を「外側の panic」と読み、try-recover の `v.inflight = saved` 復元で**消化済み panic が復活**し無関係な後続 `recover()` に届く — panic 状態機械として最悪の部類のリーク。panic state を touch する経路は全て saved/restore 対称を確認する必要がある。
- **nil-ness の分岐表に型レベル規則が吸収されていた（#434）**: `IfaceNil`/`TypedNil` の switch が nil-ness で早期 return するため、「動的型が同一 uncomparable 型なら nil 同士でも panic」という Go の型規則が値の nil 分岐に隠れて素通りしていた。「値が nil か」より先に「動的型ペアが panic 条件か」を見る前置チェック（`uncomparableDynamicTyp`）を eqlValue に置いて解消。ifaceEql 側への二重チェックは Nil-family が全て eqlValue に流すため冗長と判断して置かなかった。
- **generic unify が「値の型」を先に見ていた（#432）**: `unifyType` が引数の動的 typedef を優先し、`id(error値)` が `undefined: T`、interface 経由が `cannot use *T` に。Go の型推論は**式の宣言型**への制約解消であって値の型ではない — 動的型は spread/nil 補完の fallback に留めるべきだった。`argStatics` で「コンパイル時に分かる宣言型」を実行時に運ぶ経路を新設した。
- **同期呼出しを goroutine で実装すると panic の帰属が変わる（#448）**: `callReflectFunc` が blocking host call を helper goroutine に逃がす実装のため、`once.Do(f)` 内の `vc.Call` が「外部 goroutine からの呼出し」に見え、子 VM の panic が `failProc`/`p.fail` でプロセス全体を殺していた。「API 上は同期だが実装上は別 goroutine」の境界では panic をどの goroutine のものとするか設計で明示しないと crash 意味論が漏れる。kill site が spawn 境界と子 VM root の2箇所ある点も子セッションの発見 — 片方だけの修正では再発する。
- **経路共有の seen map が別経路の探索を汚す（#449）**: embed 探索の cycle 検出 `seen` を全経路共有すると「値として見た T」が「ポインタ経路の `*T`」の訪問を消費する。`defer delete(seen, td)` の per-path 化で各経路が自身の寄与を持つ形に。`methodSetOfU`/`methodFuncs` 双子関数が同一バグを共有していたのはコピー由来で、#453 の `methodWalkU` 一本化で構造的に閉じた。
- **「リファクタ候補」の査定が実は drift の発見だった（#453）**: 重複指摘と思って統合したら、片側だけが AST 無し型（host 由来の facade interface 埋め込み）を扱えていなかった。2実装の統合は差分の照合でもあり、差が出たら oracle でどちらが正しいか決着させるのが定石。
- **匿名 func 型の assert が kind のみで受理していた（#455 記録）**: assert switch が `KindFunc` なら全 func 値に true。子の検証で発見したが根因は別系統（typedef の signature 比較が要る）のため TODO.md 記録に留めた。

### 計画外の記録と判断

- **「standalone で発散しない」WARN の繰り返しが emit ノイズではなく実バグだった**: hunt で繰り返し出ていた「バッチ内では TRAP だが単体では PASS」系を emit-skip 幻影と決めつけて棚上げしていたが、バッチプログラム（`/tmp/difffuzz-*/genN_M/main.go` — 全 probe が `try(N, ...)` で VM を共有）を minigo+go で直接走らせ、**try() prefix の delta-debug で poisoning probe（defer 内 `*v_pp_0` nil deref）まで絞り込んだ**ところ、probe 間を跨ぐ panic 状態リーク（#436）と判明。6系統の発散バッチ全てが同じ根因で修正で全消え。教訓: 「バッチ限定の幻影」はむしろ共有-VM 状態機械のバグを照らす固有の検出面であり、WARN 分類で握り潰さずバッチプログラム単位で bisect する手順を確立した。
- **interface `==` の panic を「nil でも panic」に寄せる判断**: `any([]int(nil)) == any([]int{1})` を false と返す実装は一見 reasonable だが、Go の規則は「比較は値ではなく型に対して判定される」。pin は nil×live・live×nil・nil×nil・map・`== nil`・comparable 配列・`!=` の9 probe で境界を固定した。
- **レビュー指摘は受理前に stack tip で再現確認**: 外部レビューの2件は古い差分への指摘の可能性があるため、委譲前に `/tmp/oncebug`/`/tmp/embedbug` の repro を自分で作り stack tip で byte-for-byte 再現を確認。両方真のバグだったので「要不要判定付き」の子委譲に回した（§6.12 の「レビュー指摘は現スタックトップで再現確認してから直す」と同じ運用）。
- **2段階委譲を直列にした理由**: phase 2 の `methodSetOfU`/`methodFuncs` 統合候補は phase 1 の埋め込み `seen` バグと同一領域なので、バグ修正の着陸を待ってからリファクタに当たらせた。子には「要不要を自分で判断し優先度順に直す」権限を渡し、全件「要」判定で処理された。
- **`sync.Once` の panic 意味論を3条件で固定**: 「同期コールバックの panic は呼出し側で recover 可能」「真の外部 goroutine コールバック（`wg.Go`/`time.AfterFunc`/`go`）はプロセス crash のまま」「Once は panic 後も done 扱い（Go 仕様）」を oracle と突き合わせて境界を固定。「host call は全部呼出しに返す」と雑にやると goroutine crash 意味論自体が消える。
- **検証で見つかった新規 divergence は修正せず記録**: phase-2 子が発見した func-signature assert gap は本レビューの指摘項目ではなく根因も別系統のため、1 root cause = 1 PR の流儀通り TODO.md 記録のみの PR（#455）に切り分けて次ラウンドの入口とした。
- **hunt 枯渇をもって完了と判断**: lang 連続クリーン（発散が出た 059 の再実行を含む）+ num/text/reflect 全 PASS で補給を打ち切り。100 上限には遠く及ばず。
- **取りまとめ役の継続**: round-10 で確立した子セッション委譲の運用を踏襲 — バグ系・リファクタ系それぞれ全項目を要不要判定→修正→pin→CI 確認まで子に担わせ、自分は repro 検証・積み上げ管理・本レポートに集中。2件とも完遂。

## 6.14 実施ラウンド（round-12）: Stack #471 — difffuzz 掃討・枯渇判定・外部レビュー9件+リファクタ9件の委譲

発端は TODO.md の difffuzz 系残件（`$GOROOT/test` コーパス未走査 subdir）を「1 root cause = 1 PR」で stacked PR に積む指示（上限 100、枯渇で終了、枯れたら `gen` で補充）。成果: **Stack #471 に 37 PR（#467–#504）を構築して掃討フェーズを枯渇終了**させ、その後届いた外部レビューのバグ9件を5子セッションに委譲して修正（#510–#514）＋CI flake の根因修正（#516）、リファクタ提案9件は要不要判定付きで直列の子1件に委譲（#518–#529）。

### 実施内容

| フェーズ | 内容 | PR |
|------|------|-----|
| corpus sweep | 3子セッションで typeparam / ken・interface・chan / syntax・abi・stress・dwarf・simd を走査、自分で fixedbugs の `// run` 646件と codegen（87件全 SKIP）を処理。発散を PENDING pin として31件取り込み | [#467](https://github.com/podhmo/minigo/pull/467)–[#470](https://github.com/podhmo/minigo/pull/470) |
| serial fix 前半 | local typedef identity の宣言サイト化、iface assert のシグネチャ比較、pkg var init の依存ゲート、map range の Order 共有破壊、struct tag identity 正規化、unary const fold + `-0` の materialize、case expr の定数性保持、wide uint64 の float 変換、`complex()`/`real()`/`imag()` の const fold、nil `*[N]T` の deref 省略、huge zero-size slice の virtual backing | [#472](https://github.com/podhmo/minigo/pull/472)–[#482](https://github.com/podhmo/minigo/pull/482) |
| serial fix 後半 | nil interface vs boxed typed nil の区別、value-method の nil ディスパッチ文言、pointer receiver の lvalue storage ref 束縛、comma-ok `var a,b = rhs`、nil receiver の iface method expr、NaN sort 順、bodiless asm decl の host 実装、blank receiver tparam、`*runtime.BuiltinFunc` のジェネリック推論、ローカルジェネリックの外側型引数捕捉、minireflect の関数 typedef fn-scope、boxed typed nil の具象型保持、funclit の instantiation binds 捕捉、script struct の host `any` marshal（template 経路） | [#483](https://github.com/podhmo/minigo/pull/483)–[#496](https://github.com/podhmo/minigo/pull/496) |
| 補充（gen hunt） | 枯渇後に gen で4件の発散を採掘して pin（#497）。修正: unsigned 縮小変換のマスク順、assert panic の nil 動的型表記、nil interface の map key 正規化、nil interface cell / nil deref ref の select panic、`reflect.Zero(iface).Elem()` の invalid 返却 | [#497](https://github.com/podhmo/minigo/pull/497)–[#503](https://github.com/podhmo/minigo/pull/503) |
| 帳簿 | TODO.md の difffuzz 節32件 `[x]` 化 | [#504](https://github.com/podhmo/minigo/pull/504) |
| 外部レビュー（バグ9件 → 5子委譲） | nil interface composite key（#510）、`os.Stdout` の `*os.File` facade（#511）、deepHost 循環参照 P1 + template niladic メソッド評価（#512）、シグネチャ比較の意味論化 — alias peel + 無名 iface method-set 比較（#513）、virtual slice の表現属性化 + bounds/spread/copy（#514） | [#510](https://github.com/podhmo/minigo/pull/510)–[#514](https://github.com/podhmo/minigo/pull/514) |
| CI flake 根因 | `watchCallFrom` の nil proc レース — check-then-use の間に `ReleaseProc` が `v.proc` を nil 化（main 由来の潜伏バグ、#514 の CI で初観測） | [#516](https://github.com/podhmo/minigo/pull/516) |
| 外部レビュー（リファクタ9件 → 直列1子委譲） | 採用7・不採用1・部分採用1。判定中に実害バグ4件を発見・修正（下表）。統合ベースの merge PR（#518）を挟んで各項目を独立 diff に | [#518](https://github.com/podhmo/minigo/pull/518)–[#529](https://github.com/podhmo/minigo/pull/529) |
| 本レポート | この round-12 セクションの追記 | 本 PR |

### リファクタ提案9件の判定結果

| # | 提案 | 判定 | PR |
|---|------|------|-----|
| 1 | `typeMethodFuncs` は `methodSet` の重複 → 削除 | 採用 | [#519](https://github.com/podhmo/minigo/pull/519) |
| 2 | 値からのメソッド集合探索の二重化 | 採用 — `methodInfoOfValue` が (names, funcs, unsure) を一括返却。**実装中にバグ発見**: `type A = sync.Mutex` の alias 先の host/iface method set が見えていなかった → [#520](https://github.com/podhmo/minigo/pull/520)（pin `hostalias_methodset`） | [#520](https://github.com/podhmo/minigo/pull/520), [#521](https://github.com/podhmo/minigo/pull/521) |
| 3 | iface sig → Function 変換ループの重複 | 採用 — `ifaceSigFuncs` に3箇所集約、nil-for-empty で統一 | [#522](https://github.com/podhmo/minigo/pull/522) |
| 4 | シグネチャ比較の VM/minireflect 分散 | 採用 — `runtime.TypeResolver` 注入型の `sigComparer` に一本化（`runtime.SigTypEq`/`SigIdentical`）。#513 の `sigTypEq` は VM 側薄ラッパに縮退、minireflect `Implements` も同じ comparator を経由。**副次効果**: `reflect.Implements` が alias typedef を解決するようになり `reflect_implements_alias` の PENDING 解除 | [#523](https://github.com/podhmo/minigo/pull/523) |
| 5 | func 値からシグネチャ抽出の共通化 | 採用 — `runtime.FuncSigOf`。**バグ発見**: method expr `T.M` が `func(int) int` と表示 → `func(main.T, int) int` に修正 → [#525](https://github.com/podhmo/minigo/pull/525)（pin `methexpr_sig`） | [#524](https://github.com/podhmo/minigo/pull/524), [#525](https://github.com/podhmo/minigo/pull/525) |
| 6 | `structDataHost` が `structMember` を再実装 | **不採用** — 対象コードは #512 で全面書き換え済み（eager-eval + 呼出しバジェット = 提案が「必要」と明記した橋自体）。`structMember` へ寄せると「宣言メンバのみ投影」という明文化された挙動が変わる（昇格メンバ混入・frame 依存） | — |
| 7 | virtual slice 操作を `Slice` に集約 | 採用 — `N` 直読みを `Len()`/`Cap()` 経由に。[#514](https://github.com/podhmo/minigo/pull/514) の表現属性化の続き。**バグ2件発見**: `append(real, vslice...)` が実要素を落とす / ゼロサイズ要素 append の cap 倍加（Go は `cap=newlen`）→ [#527](https://github.com/podhmo/minigo/pull/527)（pin `append_vspread_real`, `append_zerosize_cap`） | [#526](https://github.com/podhmo/minigo/pull/526), [#527](https://github.com/podhmo/minigo/pull/527) |
| 8 | untyped const のデフォルト型変換の重複 | 採用 — `runtime.UConstNative` に共有コア化。rune タグ・complex の `GoValue` wrap は VM 側に残置（提案の制約どおり） | [#528](https://github.com/podhmo/minigo/pull/528) |
| 9 | nil iface / boxed typed nil 分類の散在 | 採用 — `runtime.IfaceTaggedNil`/`BoxedNilTyp`/`IsNilIface` に集約。#510 の `writeKeyElem` 経路も同じ分類子を通す | [#529](https://github.com/podhmo/minigo/pull/529) |

### 残りの状況

- difffuzz キューは掃討フェーズで枯渇: gen 全4ドメイン（text/num/reflect/lang）で stack tip 上 0 divergence（20バッチ・depth 6 の深掘りでも 0）、corpus 全 subdir 走査済み。PENDING 残りゼロ（`reflect_implements_alias` は #523 で解除）。
- 外部レビューのバグ9件は全件真の指摘として修正済み（1件は未観測の P1 — `deepHost` の循環参照でプロセス死）。リファクタ9件も判定・実装まで完了。
- Stack #471 は 55 PR + 本レポート PR。境界クラス（unsafe.Pointer、GC fidelity、gcgort、*.dir、cgo、スループット HANG）は引き続き対象外。

### 不備の振り返り

- **`IfaceNil{interface-kind}` = 「動的型なしの nil eface」という不変条件の適用漏れ**: CanonicalKey・panic 表記・reflect.Elem・member select の4サブシステムに正しく入れたが、複合キーの `writeKeyElem` で interface-kind にも `typ:nil` を付けてしまい「`==` では等しいのに map key が別」に（#510）。ルールを宣言したとき、構造体フィールド経路にも同じ分類が要ることを書き切れていなかった — §6.13 の nil-ness 分岐表と同根。#529 で分類子自体を共有化したので再発は構造的に抑止。
- **`deepHost`/`structDataHost` は「投射」の設計欠落が2層あった**: 循環参照の visited-set 不在（P1 — Go では `map[string]any` に循環が合法なのにプロセス死亡）と、map 投影では niladic メソッドが「呼ばれない」ことの見落とし（template の field lookup は map 値をそのまま取る）。後者は #496 時点で既知の制約と書いていたが実害級と判明 — 「書いた制約」は「検証済みの境界」ではない。eager eval は seen-map・shape filter・呼出しバジェット64の3重で境界化した（`TestNetHTTPRoundtrip` で史上2度目の eager-eval ハングを回避）。
- **virtual slice は「`N>0` で判定」ではなく表現属性であるべきだった**: `s[:0]` で CapN 消失、bounds 非検査、append が cap を捨てる、spread/copy が論理長を無視 — 4件全て「virtual 判定が Elems を見る局所実装」に集約される。`Virtual()`/`Len()`/`Cap()` の一本化で構造的に閉じた（#514→#526）。それでも `append(real, vslice...)` の要素落としは残っていた（#527）— アクセサ化後も直読み箇所が残る限り抜ける。
- **unsigned 縮小変換の適用順**: `uv > MaxInt64` の boxing を幅マスクより先にやっていたため `uint16(-3)` が `2^64-3` に（#498）。「変換は (a) 幅にマスク (b) 収まらなければ box」の順序を明記しておくべきだった。
- **シグネチャ比較を spelling で実装したのが抜け道**: alias peel と匿名 iface の method-set 順序を通さない `TypIdentical` は「等しいものを違う」と言う方向にバグる（#473 → #513 で semantic 比較に置換、`any(<-chan int).(chan int)` の逆向き偽陽性も同時に潰れた — chan direction を見ていなかった）。#523 で comparator が runtime の単一実装になったため、今後の呼出し側追加は自動的に意味論比較になる。
- **`os.Stdout` の facade 化で「Write 以外のメソッドも host 値の API」という不変条件を落とした**: リダイレクト目的の最小 wrapper が `Name()`/`Fd()`/`Stat()` を trap に。埋め込み委譲＋`Write`/`WriteString`/`ReadFrom` だけオーバーライド（#511 — `ReadFrom` は `io.Copy` が dst の ReaderFrom を優先するため、置かないと fd 1 直書きに回帰する）。
- **sibling-stack のブランチ構造を委譲プロンプトに書いていなかった**: 修正群のブランチは全て旧 tip の「兄弟」として切られているのに、リファクタ委譲時には procrace 1本だけをベースとして渡した。子が `sigTypEq` の不存在を自ら検出して部分採用に留めたのは幸いだったが、本来はマージ済み統合 tip を先に作って渡すべきだった。同じ合流内容を必要とする後続作業には `#518` のような integration merge を先に置く運用にする。

### 計画外の記録と判断

- **レビュー9件の子委譲分割**: ファイル領域で5件に束ねた（structDataHost 系・os.Stdout・virtual slice・map key・シグネチャ比較）。SWE-2 の5並列上限を踏み、完了した子は `sleep` させてスロットを開けてから5件目を起動 — 完了通知が来ても running 扱いで枠を占有し続けるのが罠。
- **CI の flake を根因まで掘った（#516）**: #514 の `test` job が `watchCallFrom` on nil proc で SIGSEGV。main にもある潜伏レース（`v.proc` の check-then-use）で、直すべき根因として stack 最上位に1 PR 追加。再現は非決定的なので、guard を「capture してから nil 判定」に変えて構造的に消した。#514 側のジョブは空コミットで再実行。
- **リファクタ委譲は直列**: 提案同士が dispatch.go/vm.go のメソッド機構・virtual slice・シグネチャ比較の同一領域に重なるため、ユーザーの助言通り1セッションの直列に。要不要判定権限つき（提案は stack の古い状態を見ている可能性があり、sigident・vslice・hostmarshal の着地で既に一部実現済み — 実際 item 6 は不採用だった）。
- **リファクタは「読み比べ」で新バグを顕在化させる**: 判定・共通化の過程で4件の実害バグ（hostalias_methodset、methexpr_sig、append_vspread_real、append_zerosize_cap）が見つかった。重複コード解消は「2経路の差分」を強制的に読ませるため、差分バグの発見装置としても機能する — §6.12 と同型の観測。
- **stacked PR 上での sibling→統合**: リファクタチェーンは複数 sibling 修正の合流内容を必要としたため、`#518`（統合 merge、自身の変更なし）を stack に1枚挟んで上位11件を独立 diff に保った。sibling が下から順に landed すれば #518 は空になり自然解消される。
- **「全緑」の運用**: バグ修正群の着地 + 各 PR の CI 緑（flake 1件は根因修正済み）を待ってからリファクタを起動した。

## 6.15 実施ラウンド（round-13）: Stack #549 — difffuzz 再開・外部レビュー5件の直列処理・リファクタ委譲・範囲上限20で打ち切り

発端は前回同様 TODO.md の difffuzz 系残件の「1 root cause = 1 PR」直列掃討指示（上限は当初 50 → 途中で 20 に変更）。成果: **Stack #549 に 19 修正 PR（#547–#568）＋リファクタ 6 PR（#570–#575）＋本レポート**。途中で届いた外部レビュー（P2 バグ5件 + リファクタ提案7件）のバグ側を全件自前で修正し、リファクタ側は CAP 到達時に子セッション1件へ委譲する方針で後送りにした（ユーザー指示）。委譲の実施: [子セッション](https://app.devin.ai/sessions/f2ce53918c174e8da6626e4c0255a69f) が採否判定つきで実装し、自分は差分検証と stack 化に集中した。

### 実施内容

| フェーズ | 内容 | PR |
|------|------|-----|
| 前回残件 | nil slice ptr-receiver（#547）、`append(s,nil)` の要素型 zero（#548）、`hostcycle_struct` の sync.Pool flake（#550 — 評価器依存の pool 再利用を sync.Map に置換） | [#547](https://github.com/podhmo/minigo/pull/547)–[#550](https://github.com/podhmo/minigo/pull/550) |
| corpus triage | binop eval-order の二相評価化（#551）、`print` 非文字列間の空白除去 + `want.stderr` 対応（#552）、multi-assign の implicit-indirection pin（#555）、MapIter の canonical-key probe（#556）、const/var の init 順（#557） | [#551](https://github.com/podhmo/minigo/pull/551)–[#557](https://github.com/podhmo/minigo/pull/557) |
| corpus triage 続き | sort の nil slice 受容（#558）、localtype の外側型引数（#559）、`range *p` の lazy pointer iteration（#560）、zerobase 要素アドレス比較（#561） | [#558](https://github.com/podhmo/minigo/pull/558)–[#561](https://github.com/podhmo/minigo/pull/561) |
| 外部レビュー 5件 + TODO1件（自前・直列） | sort の typed-nil 型チェック（#562）、massign pin の Cell→全 ref 種一般化（#563）、array range snapshot + 2operand 物質化（#564、レビュー2件目と array-value copy の TODO を一根因で）、rangeStarLazy の call/receive 検出（#565、`lenOperandCalls` 再利用）、closure 内 localtype の外側 instantiation（#566 — `Function.OuterTParams` + `outerTypeArgs` 共有化） | [#562](https://github.com/podhmo/minigo/pull/562)–[#566](https://github.com/podhmo/minigo/pull/566) |
| corpus 新規 | host nil chan の select arm trap（#567 — `context.Background().Done()` が `runtime.Nil` に潰れる。typeparam/orderedmap.go が通過）、instantiation の alias peel（#568 — `T[GlobalInt]` が `main.Int` に） | [#567](https://github.com/podhmo/minigo/pull/567), [#568](https://github.com/podhmo/minigo/pull/568) |
| 帳簿 | TODO.md に12件 `[x]` と3件 `[ ]`（`·N` スコープマーカー・timeout 仕分け・corpus 残存ファミリ）を追記 | [#569](https://github.com/podhmo/minigo/pull/569) |
| リファクタ（子セッション委譲） | `hoistEagerOps`（binaryOperands/callArgs の二相評価共有・#570）、`bindArgs`（name/arg→binds fold 5箇所・#571）、`resolveLitType`（peel+resolveName+tspec walk 共有・#572）、`storageShape`+`ast.Unparen`（peel ループ11箇所・#573）、`Map.SnapshotKeys`/`LookupCanonical`（NaN-key 規約を runtime.Map に集約・#574）、`TypeDef.InstArgs`/`InstArg.Bound`（outer→own 列挙共有・#575）。sort nil-slice 判別ヘルパー提案は #562 の `nilSliceArg` で実質済みと判定 | [#570](https://github.com/podhmo/minigo/pull/570)–[#575](https://github.com/podhmo/minigo/pull/575) |
| 本レポート | `### 6.N` → `## 6.N`（1ラウンド1章化）+ 本章 | 本 PR |

### 計画外の記録と判断

計画時の仮説・設計と実施後の理解がずれた点、および計画に無かった事象への判断。不一致は悪いものではなく、実態を後から理解して考慮した結果 — そのとき何を決めたかを明示する。

- **orderedmap の crash 仮説はすり替わっていた**: 「FieldRef↔DerefRef の相互 unwrap で無限再帰」という持越し仮説で着手したが、最新 tip で再現すると stack 中の修正で症状が変化しており `channel operation on runtime.Nil`（host nil chan の select arm）に化けていた。→ 実際の根因は host nil chan として #567 で修正。crash 経路そのものは再現しなかったため仮説は保留扱いとし、まず repro 取り直しを行う手順に変えた。
- **`range *p` の lazy/eager 境界は1軸ではなかった**: 当初設計は OpIter のフラグ（operand 数のみ）で切る想定だったが、プローブで gc の実際の境界が「AST 形 × operand 数 × 要素読み取り有無」の3軸と判明。→ フラグ設計を撤去し、NilArr の遅延 panic に委ねる形に変更（#564/#565）。
- **zerobase 等値の境界はオブジェクト種別依存**: 当初の想定「ゼロサイズ要素は全て同じアドレス」は誤りで、6本のプローブが実際の分岐（同一配列内 true / 別 array オブジェクト false / slice 要素はコンテナ跨ぎ true / `new(zerosize)` 独立）を示した。→ IndexRef↔IndexRef に限定して実装（#561）。
- **alias は identity 済み・spelling 未処理だった**: `T[GlobalInt]` は identity 側が既に正しく、canonicalization が keyOf にしか入っていなかった。→ binds 書き込み入口（`instantiate`）で peel するのが両系に効く一点と判断（#568）。
- **`·N` スコープマーカーは見た目より大きい**: nested.go の残差は表示問題だけと見込んだが、機構は noder `declCollector` → `qualifiedIdent` の `name·gen` 埋め込みで、宣言順 gen 採番を compile→runtime に通す工作が要る。→ 本ラウンドのスコープ外として TODO に `[ ]` で記録し後送り。
- **CAP の途中変更（50→20）とリファクタ委譲**: ユーザー指示で上限が引き下げられ、バグフィックスは20本で打ち切り。レビューのリファクタ側は子セッション1件への委譲に切替え（同じくユーザー指定）、自分は検証役に回った。→ 7提案は6採用・1既済（sort nil 判別は #562 の `nilSliceArg` で済み）と判定。
- **検証で「挙動として保持」と判断した癖**: `lenOperandCalls` の parens 透過（`ast.Inspect` 経由なので剥がし済み/未剥がしどちらの expr を渡しても同値）、`bindsKeyOf` が own TParams 全未束縛時に末尾 `;` を出す表示癖、`instArgsSpelling` が `len(own)>0` のときだけ `;` で区切る癖 — 全て明示的に保持した。`bindArgs` の unguarded→guarded 統一2箇所は全経路で arity trap が先行するため到達不能差分（crash→trap の改善側）。`resolveLitType` の fuel（8 vs 4）は意図的な探索深さ差として共有化せず呼出側に残した。
- **レポート構造の正規化を本 PR に同梱**: `### 6.N` が round-1 由来の `## 6.` 見出しの子に全て入れ子になっていた問題を、`## 6.N` 昇格で1ラウンド1章に解消。`§6.N` 相互参照と GitHub アンカーを守るため見出しテキストは変更せず、運用規約（新ラウンドは `## 6.<next>`）を `## 6.` 直下のメモに記録した。

### 残りの状況

- Stack #549 は全26本（修正20 + リファクタ6）マージ済み。純リファクタ7提案は消化済み。
- 変わらず残件: `·N` スコープマーカー（機構特定済み・実装後送り）、corpus timeout 仕分け約19件、panic-message/traceback/identity 系差分ファミリ、issue66575/35576/59411。境界クラスは対象外。

### 不備の振り返り（メモ）

- docs 帳簿コミットを fix ブランチに誤って積み force-push で分離 — stacked ブランチへの push 前に `git log -1` で先端を確認する習慣づけ。
- 親子セッションの ~/memory 同時書き込みが conflict — 委譲時は memory 書き込みを親に限定する旨を明示するとよい。

## 6.16 実施ラウンド（round-14）: Stack #579 — difffuzz corpus 残件掃討・19 fix・全体差分レビュー10件・CAP20 で打ち切り

発端は前回同様 TODO.md の difffuzz 系残件（corpus キューファミリ中心）の「1 root cause = 1 PR」直列掃討指示（CAP=20）。成果: **Stack #579 に difffuzz 修正 19 PR（#577–#599）＋ レビュー由来 10 PR（#601–#610）＋ 本レポート**。レポート作成前に全体差分（main〜stack 最上位）のコードレビューを子セッション1件に委譲し（ユーザー指定のフェーズ）、返答を「バグ優先 → リファクタ」の順で精査して対応した。レビュー発見の修正は CAP 対象外との追認（ユーザー明示）を受けて積んだ。

### 実施内容

| フェーズ | 内容 | PR |
|------|------|-----|
| corpus 等値性 | chan の動的==wrapper 比較（#577）、recover 済み panic フレームの結果ゼロ値（#578）、配列長の定数評価（#580）、ブランクフィールドの要素評価（#581）、func-local 型の無名型スペリング（#582）、ジェネリック map キーの型引数保持（#583）、名前付き配列の underlying 等値（#589） | [#577](https://github.com/podhmo/minigo/pull/577)–[#583](https://github.com/podhmo/minigo/pull/583), [#589](https://github.com/podhmo/minigo/pull/589) |
| corpus traceback | `Frame.Function` の修飾名（#584）、`//line` 未記録ファイル名の `??`（#585）、bounds check の `[` 位置（#586） | [#584](https://github.com/podhmo/minigo/pull/584)–[#586](https://github.com/podhmo/minigo/pull/586) |
| corpus printing/const | host uint64 結果の int64 bits 読み（#587）、typed const 変換の定数ドメイン保持（#588）、defer 中 `runtime.Caller` の gopanic 経路（#591）、script `fmt.Formatter` の verb 委譲（#592）、fmt の深さ上限撤去（#593） | [#587](https://github.com/podhmo/minigo/pull/587)–[#593](https://github.com/podhmo/minigo/pull/593) |
| corpus 残件 | iface メソッド値の nil レシーバ lazy bind（#594）、iota 参照の定数ドメイン保持（#597）、assert 失敗の scopes/packages 曖昧さ解消サフィックス（#598）、FieldRef の解決スロット等値（#599） | [#594](https://github.com/podhmo/minigo/pull/594), [#597](https://github.com/podhmo/minigo/pull/597)–[#599](https://github.com/podhmo/minigo/pull/599) |
| 全体差分レビュー（バグ） | 子セッション指摘 B1–B9 の精査: B1/B2（chan/array の `eqlValue` 緩和が interface-pair にも効く退行 → `BinEqlIface` で静的==と動的ペア==を分離・#601）、B3（ネスト要素の script Formatter が live state に直書き・#602）、B4（uncomparable 要素 panic が内側型を名指し・#603）、B9の一部（named struct vs 無名 literal の静的==・#604）、B5（`Frame.Function` の `(*T).P` 表記・#605）、B7/B8（`:=` セルの inferred declared type が再代入で失われる・#606） | [#601](https://github.com/podhmo/minigo/pull/601)–[#606](https://github.com/podhmo/minigo/pull/606) |
| 全体差分レビュー（再実装・リファクタ） | `runtime.AnonFieldName`/`numericBasicName` の重複解消（#607）、index 系 op の `[` 位置統一の残り（#608）、合成 gopanic CallSite の命名（#609）、`ifaceOperand` → `isIfaceExpr` 統合＋ nil iface 短絡（#610） | [#607](https://github.com/podhmo/minigo/pull/607)–[#610](https://github.com/podhmo/minigo/pull/610) |
| 帳簿 | TODO.md の corpus キュー項目を更新（解消22件・残 bug301/issue4562/issue54467・境界 bug260+cgo 5件を記録）＋ レビュー残件2件を `[ ]` で追記（B6 の local nil method-value panic、B9 の定数 overflow 非検査） | 本 PR |
| 本レポート | 本章 | 本 PR |

### レビュー指摘の判定結果

| 指摘 | 判定 | PR |
|------|------|-----|
| B1 `any(chan<-T) == any(chanT)` が true（退行） | 採用 — B2 と一根因 | [#601](https://github.com/podhmo/minigo/pull/601) |
| B2 `any(A{}) == any(B{})` が true（退行） | 採用 — B1 と一根因 | [#601](https://github.com/podhmo/minigo/pull/601) |
| B3 ネスト script Formatter が live state に直書き | 採用 | [#602](https://github.com/podhmo/minigo/pull/602) |
| B4 uncomparable panic が外側ではなく要素型を名指し | 採用 | [#603](https://github.com/podhmo/minigo/pull/603) |
| B5 `Frame.Function` が `(*T).M` を返さない | 採用 | [#605](https://github.com/podhmo/minigo/pull/605) |
| B6 local `var i I = (*T)(nil); i.M()` の panic 文言 | **記録のみ** — pre-existing（package var 側は gc どおり `valuemethod_nilptr` で pinned、local 側の分岐が残差）→ TODO `[ ]` | — |
| B7 `x := int8(1); x = 300` でタグ喪失 | 採用 — B8 と一根因（inferred declared type をセルに保持） | [#606](https://github.com/podhmo/minigo/pull/606) |
| B8 named-to-named の const 代入を受理 | 採用 — B7 と一根因 | [#606](https://github.com/podhmo/minigo/pull/606) |
| B9 補助確認群 | 一部採用 — `s == struct{a int}{1}` は #604、`any(A(1))==any(int(1))` は #601 で解消。残差（`int8(300)` 受理）は pre-existing で TODO `[ ]` | [#601](https://github.com/podhmo/minigo/pull/601), [#604](https://github.com/podhmo/minigo/pull/604) |
| `runtime.AnonFieldName` が `embeddedFieldName` と逐語同一 | 採用（vm 側を削除して runtime 側に統一） | [#607](https://github.com/podhmo/minigo/pull/607) |
| `numericBasicName` が `builtinTypeName` の subset を再列挙 | 採用（合成に置換） | [#607](https://github.com/podhmo/minigo/pull/607) |
| `OpIndexOK`/`OpInstantiate` の `.Pos()` 残置 | 採用 — `x.Lbrack` 統一の残り | [#608](https://github.com/podhmo/minigo/pull/608) |
| `Line: 859` ハードコード（合成 gopanic site） | 採用 — `gopanicCallSite` 定数化＋由来コメント | [#609](https://github.com/podhmo/minigo/pull/609) |
| `foldArrLen` が共有 AST `at.Len` を書き換え | **不採用** — `[1e1]`→`[10]` の正規化は冪等で全 consumer が同一値を期待するため実害なし。TypeDef 側キャッシュは二重管理源を増やすだけ | — |
| `ifaceOperand` が `isIfaceExpr` の部分集合を再実装（自分の検証で発見） | 採用 — 統合したうえで declared-iface-var の残差も解消 | [#610](https://github.com/podhmo/minigo/pull/610) |

私の検証で確認した差分上の注意点（全て挙動保持または gc 同方向）:

- `isIfaceExpr` への置き換えで `e == nil` が `BinEqlIface` 経路に載るようになったため、`ifaceEql` に nil 短絡（`isIfaceNilValue`）を追加 — pair 規則は動的型があるときだけ適用。`TypedNil` が `coerce` で `IfaceNil{*T}` に箱化されるため `IfaceNil.Typ` が interface-kind のときだけ「無印 nil」と判定する
- その `isIfaceExpr` 拡張はパッケージ変数 fallback が shadowing を見ない穴を持っていた（Devin Review 指摘・実害確認済み: `var s any` + local `var s []int` で `s == nil` が false）— local binding が存在するときは package index を見ない条件を追加し、ピンに同ケースを追加
- `stampInferredTyps` は `:=` LHS に `OpCoerce` を emit するだけで `inferredTypExpr` が nil の場合のみ合成 InterfaceType を刻む — `i := any(x)` のセルは interface 型のままなので `i = 300` が int に置き換わる挙動を変えない
- `assignCell` の named-to-named trap は `ct.Kind != KindInterface` でゲート — interface 宛の代入は従来どおり素通し
- `OpIndexOK`/`OpInstantiate`/`typeExpr` の位置変更は trap 位置のみで値経路不変
- `hoistEagerOps` の scratch local への `markIface` は `isIfaceExpr(call)` のときのみ — 非 interface operand の codegen は不変

### 計画外の記録と判断

計画時の仮説・設計と実施後の理解がずれた点、および計画に無かった事象への判断。不一致は悪いものではなく、実態を後から理解して考慮した結果 — そのとき何を決めたかを明示する。

- **「corpus キュー = 個別根因」の想定は過大だった**: 着手時に22件近い queued slug を個別根因のつもりで並べたが、機構レベルでは等値性・定数ドメイン・traceback・fmt・bind の少数に集約された。実際11件の queued corpus ファイルが専用修正なしで `go run` 一致になった（bug254/266, issue21879/22662/29504/31546/35576/46591/50190/59411/66575）。→ 各 slug を直列に拾う代わりに、機構単位で dedup してから pin する手順に切り替えた。
- **fmt の深度上限は防御的な誤りだった**: `depth > 8 → "..."` は無限再帰防止の妥当な安全弁と見ていたが、ホスト Go は循環 map でスタックオーバーフローする — 上限自体に Go 側の根拠がなかった。→ 上限撤去で gc 忠実（クラッシュ同値）に倒した（#593）。
- **script `fmt.Formatter` の呼び出し優先度は Stringer 家族ではなかった**: `fmtValue.render` で Formatter は Stringer より先に、かつ %T/%p 以外の全 verb で参照される。`zeroState`（Go 側で Formatter が起動されない描画）を識別して除外しないと過剰委譲になることを実装中に確認。→ Formatter を Stringer ブロックの前に、zeroState 除外つきで挿入（#592）。
- **nil レシーバのメソッド値パニックは bind 時ではなく invoke 時**: `memberOfType` で値メソッド値の生成を eager に拒否する設計を仮定していたが、Go は `x := i.M` の束縛を許し `x()` の呼び出しでパニクる。→ panic を `prepFrame` の BoundMethod ケースへ移動（#594）。panic メッセージ中の `T.T.M` 重複は `Function.Name` が既に `T.M` 形を持つことに後から気づいた修正。
- **defer 中の `runtime.Caller` チェーンは合成フレームを含む**: 「unwindDepth より上の live frame が caller」という仮説で読んだが、ホストプローブで実際のチェーンは `g → gopanic (panic.go:859) → f@panic-site → main` と panic グループごとの合成 gopanic サイトを挟むことが判明。→ `CallerPCs` に panic グループ単位の合成 CallSite 挿入に設計変更（#591）。
- **iota の float 化は read 側で防ぐのが正解だった**: 隠し local スロット経由で iota が int64 物質化するため `1.0/(iota+N)` が float64 に落ちる — 当初は格納側（UConst を local に入れる）を考えたが、storage は concrete value しか持てない構造で不可能と判明。→ `c.iotaVal` を const-spec コンパイル区間だけ有効にし `getRef` で `OpConst` に読み替える read 側変換を採用（#597）。また `InitFunc` は iota スロットを eager に宣言するが `fs.iota` を立てていない — 宣言直後に設定する必要があった（function-local 経路の `iotaSlot()` しかセットしていなかった盲点）。
- **`·N` スコープマーカーは2つの仕事に分かれた**: issue26094 は機構全体（宣言順 gen 採番）を要すると見込んでいたが、ユーザー可視の差分は assert 失敗メッセージの `(types from different scopes)` サフィックスだけだった。→ サフィックスのみ実装（#598）、`·gen` 表示マーカー本体は引き続き `[ ]` で後送り。
- **FieldRef 等値に zerobase 畳み込みは不要だった**: IndexRef のゼロサイズ要素 zerobase ルールをそのまま FieldRef に持ち込む実装を最初に書いたが、Go では `&x.z != &y.z`（別 struct 内のフィールドはゼロサイズでも別アドレス）。zerobase 共有は standalone 確保（Cell 系）に限られる。→ 畳み込みを除去し (struct, index) のスロット一致のみにした（#599）。
- **bug301/issue4562 は修正ではなく記録に**: 実害は `[recovered]` 併記を含む panic traceback の表示形式のみで動作意味は一致（recover の非nil性は合っている）。CAP 残り枠の配分としてフォーマット追従は割に合わないと判断し、TODO に残件として記録。
- **レビュー B1/B2 は「新バグ」ではなく本ラウンド修正の適用漏れだった**: `eqlValue` 緩和系の修正（#577 chan/#589 array）が静的==と interface-pair== を分けていない前提で書かれていた — 動的型ペア比較では identity 厳格が必須という区別が設計に無かった。→ `BinEqlIface` op を導入して2経路を構造的に分離（#601）。「値比較を緩める」ときに経路全数ではなく目の前の経路だけを見ていたのが原因で、以後の緩和系修正は「この比較は static か pair か」の確認を付ける。
- **B6 は「修正しない」を明示判断**: local iface var の nil method-value panic 文言差分は pre-existing で、本ラウンドの修正とは無関係。CAP 対象外だがスタックの複雑度を増やす意義が薄いため TODO `[ ]` 記録に留めた。
- **foldArrLen 不採用は「提案の前提がずれていた」例**: レビュー指摘は「共有 AST への暗黙書き換え」を問題視したが、正規化は冪等で全 reader が同一値を見るため実害なし。提案が stack のどの状態を見ているかを確認してから採否すること（§6.15 と同型）。
- **自分の差分内に再実装が残っていた**: `ifaceOperand`（#601 で入れた判定）は `isIfaceExpr` の部分集合 — レビューの「既存関数の再実装をしていないか」問いを自分の diff に向けて #610 で統合。統合が新たな差分（declared iface var ==、shadowing、nil iface）を3件露呈させたので全部同 PR で潰した。

### 残りの状況

- Stack #579 は 30本（修正19 + レビュー由来10 + 本レポート）。difffuzz 採掘は CAP=20 で打ち切り、レビュー発見の修正は枠外として積んだ。未マージ。
- 残件: `·N` スコープマーカー本体（実装後送り中）、corpus timeout 仕分け約19件（未実施）、bug301/issue4562 の traceback 形式、issue54467（未検証）、レビュー由来の B6（local nil method-value panic）・B9 残差（定数 overflow 非検査）。境界クラスは記録のみ。
- difffuzz TODO 系キューは corpus 側がほぼ枯渇（gen hunt / corpus sweep の新規採掘は CAP のため次ラウンド）。

### 不備の振り返り（メモ）

- iota 修正の中間プローブファイルが古い内容のまま走り `f := iota` で両系に蹴られた — 検証プローブは使い回さず毎回新規に書く。
- FieldRef の初版は zerobase 畳み込みまで入れて pin で自己矛盾を検出 — 実装前に「Go で別 struct のゼロサイズフィールドが等しいか」を先にプローブしておけば一手省けた。
- `isIfaceExpr` 拡張の初版は nil iface 等値と package-var shadowing を同時に退行させた（前者はピンで自己検出、後者は Devin Review 指摘）— 「判定器を広げる」変更は false-positive 側（非 iface を iface と誤認する向き）のプローブを先に書くべきだった。

## 6.17 実施ラウンド（round-15）: Stack #615 — difffuzz 残件掃討・CAP10・全体差分レビュー4件+クリーンアップ5件・Devin Review 追随

発端は round-14 同様、TODO.md の difffuzz 系残件を「1 root cause = 1 PR」で直列掃討する指示（CAP=10、途中 stack #618 の main マージに伴う全再 rebase あり）。成果: **Stack #615 に difffuzz 修正 10 PR（#612–#631）＋ レビュー由来 5 PR（#632–#636）＋ 本レポート**。レポート作成前の全体差分レビューを子セッション1件に委譲し（ユーザー指定フェーズ）、返答をバグ優先→リファクタの順で要否判定・対応した。レビュー対応は CAP 対象外（ユーザー明示の運用どおり）。

### 実施内容

| フェーズ | 内容 | PR |
|------|------|-----|
| difffuzz/CAP 修正 | 配列 store が共有 backing を汚す（#612）、int/float リテラルを materialize 境界まで UConst に（#614）、local iface var の nil メソッド呼び出し panic（#616）、`·gen` スコープ index スペリング（#620）、deferred panic が残り defer を止める（#621）、`[recovered]` チェーン（#622）、外側型引数スペリング（#623）、untyped const operand の型採用（#628）、iface ペア比較の動的型判定（#630）、float32/complex64 定数変換の丸め（#631、corpus bug470） | [#612](https://github.com/podhmo/minigo/pull/612)–[#631](https://github.com/podhmo/minigo/pull/631) |
| 全体差分レビュー（バグ） | 子セッション指摘 B-1〜B-4 の精査: B-4（float32/complex64 変換の二重丸め → exact→f64→f32 経路を `Float32Val` 一発丸めへ・#632）、B-1（`a[i][:]` が要素 read コピーを bind → `OpIndexRef` B=2 の ref-or-value + `overwriteArrayElems` 再帰・#633）、B-2（表現不能 untyped const が被演算子型に採用されず wrap → `materializeConstErr` 経由の trap・#634）、B-3（型付き定数式が wrap 域で評価される → `constBinary` 正確域 fold + unary `-` の reject・#635） | [#632](https://github.com/podhmo/minigo/pull/632)–[#635](https://github.com/podhmo/minigo/pull/635) |
| Devin Review 追随（#634 上） | 自動レビュー指摘2件を実証して修正: 多段 named チェーン（`type A B; B int8`）の overflow 非検査 → `peelNamed` 経由へ、`1e300 + c64` が complex64 +Inf を物質化 → complex64 アームに有限性検査。追加分として `1.5 + intvar` が 9.5 を返す裸スカラー/GoValue オペランド側の同根因ギャップも `scalarOperandTypedef` で閉じた | [#634](https://github.com/podhmo/minigo/pull/634)（追加コミット） |
| レビュー由来クリーンアップ | `runtime.BasicNameOf` 公開して `float32Tag`/`complex64Tag` と `basicNameOf` を統合、`withInstArgs`/`localHeadTypedef`/`funcTypedefOf`/`materializeOperandConst` 抽出、`v.draining` の defer 化（防御的） | [#636](https://github.com/podhmo/minigo/pull/636) |
| 帳簿 | TODO.md の各項目を `[x]` 化（pin slug 併記） | 各 PR |
| 本レポート | 本章 | 本 PR |

### レビュー指摘の判定結果

| 指摘 | 判定 | PR |
|------|------|-----|
| B-1 ネスト配列の全体代入で内側エイリアスが切れる | 採用 — 2根因に分解（要素 read コピー + 全体 store の非再帰） | [#633](https://github.com/podhmo/minigo/pull/633) |
| B-2 表現不能 untyped const が wrap される | 採用 — `adaptConst` の reject ゲート + 裸/GoValue・多段チェーン・complex64 Inf の拡張 | [#634](https://github.com/podhmo/minigo/pull/634) |
| B-3 unsigned 定数式が wrap 域で評価される | 採用 — both-const fold の正確域化、`-`/`^` の非対称を修正 | [#635](https://github.com/podhmo/minigo/pull/635) |
| B-4 float32/complex64 変換の二重丸め | 採用 — `constant.Float32Val` 一発丸め（tie 値で確認） | [#632](https://github.com/podhmo/minigo/pull/632) |
| `v.draining++`/`--` が panic でリーク（観測不能・防御） | 採用 — defer 化（#636 内） | [#636](https://github.com/podhmo/minigo/pull/636) |
| `float32Tag`/`complex64Tag` が `basicNameOf` の再実装 | 採用 — `runtime.BasicNameOf` を公開して両側から寄せた | [#636](https://github.com/podhmo/minigo/pull/636) |
| `convertConst`/`constToBasic`/`scalarConst` の変換テーブル重複 | **不採用** — B-2/B-4 後に残るのはオペランド形状別のディスパッチ表（name/typedef/bare value）であって、実算法自体は `fitsIntConst`/`constFloat`/`Float32Val` に既に共有済み | — |
| `typeOfValue` vs `argTypedef` の関数系 case 逐語複製 | 採用 — `funcTypedefOf` 抽出 | [#636](https://github.com/podhmo/minigo/pull/636) |
| `inInstArgs` 伝播の複製（4箇所） | 採用 — `withInstArgs` | [#636](https://github.com/podhmo/minigo/pull/636) |
| `boundTypedef → LocalTypes` フォールバック複製（2箇所） | 採用 — `localHeadTypedef` | [#636](https://github.com/podhmo/minigo/pull/636) |
| binaryOp unsigned パスの constPayload→materializeConst 同形 | 採用 — `materializeOperandConst` | [#636](https://github.com/podhmo/minigo/pull/636) |

### 計画外の記録と判断

計画時の仮説・設計と実施後の理解がずれた点、および計画に無かった事象への判断。

- **B-9（untyped const overflow）は「境界に range check を足す」話ではなかった**: 着手時は `int8(300)`/`var x int8 = 300` の変換境界でガードを入れるだけと見ていた。実態は int/float リテラルが `literalValue`/`constOperand` で即座に bare int64/float64 に物質化されており、定数と変数を区別する情報が境界に到達する時点で既に失われていた。→ 「全定数を materialization 境界まで `*runtime.UConst` に保つ」不変条件に一本化（rune/complex/巨大 float が既に従っていた規約と同じに揃えた形）。約10箇所の境界それぞれで materialize する設計になった。個所直しでなく規約統一だったと後から整理できた。
- **B-1 の根因は1つではなく2つあった**: レビューは `overwriteArray` のフラット `copy` を指したが、実装中のプローブで「`x[0][:]` の bind 自体が要素 read コピー（`elemRead`→`coerce`→`valueCopy`）を拾う」という別機構が先に効いていると分かった。→ コンパイル側に `OpIndexRef` の B=2（ref-or-value: IndexRef が取れる時だけ ref、なければ要素値）を新設し、store 側は `overwriteArrayElems` の配列要素再帰で対応。単一修正では両半分を直さないと一致しないことを確認してから2箇所に分けて直した。
- **`m[k][:]` は gc でもコンパイル拒否だった**: ピン作成時に map-of-array の `m[0][:]` を入れたら gc が `cannot slice unaddressable value` で弾く — map 要素はアドレス不能なので「ref が取れるときだけ ref 化」という B=2 のフォールバック設計そのものが gc 準拠だった。→ ピンからは外し、map-of-string の `ms[0][1:]`（合法: 文字列要素のスライスは読み取り）だけ残した。pin には gc でコンパイルできるコードしか置けない制約をここでも再確認（const_overflow の先例どおり拒否側はコメント記述）。
- **B-2 と B-3 は同じファイルでも別機構だった**: レビューは「定数ドメインのエッジ」として一括りだったが、`adaptConst`（untyped const が被演算子型を採用できない→wrap）と `binaryOp` の const-expression fold（両辺定数なら正確域で評価→range check）が別々に壊れていた。`uint(4) - 8`（trap）と `v - 8`（wrap）の区別を保つため、`binaryOp` 先頭で `constPayload` をスナップショットしてから unwrap する構成にした — Named の unwrap 後では「typed const（Named{tag,UConst}）」と「格納済み変数（Named{tag,5}）」を区別できない。これは `#615` で UConst を Tag 内に保持する設計にしたからこそ書けた判別（B-4 の payload 丸めと同じ表面）。
- **変数セルが UConst を保持しないことを先に検証した**: B-3 の fold が `x := int8(100); x + 100` を誤って定数式扱いしないか — `x` の cell が UConst payload を残すなら誤 trap になる。実プローブで `assignCell`→`coerce` が格納時に物質化することを確認してから fold を入れた（回帰では `x + 100` が -56 に wrap することを assert）。
- **`-` と `^` の非対称は unary の unsigned fallback に潜んでいた**: `unaryOp` の unsigned フォールバックは「const 域の結果が表現不能なら materialize して concrete op を走らせる」で `-uint(5)` まで許していた。`^uint(0)` は定数域の -1 が uint では maxuint に写るのでフォールバックが必要、`-` は gc の reject。→ フォールバックを `op == UnXor` に限定。`uint(4) - 8` と `-uint(5)` と `^uint(0)` の3項でしか差が出ないエッジ。
- **testdata/fuzzfix が gc 非合法なコードを抱えていた**: `ShiftUintCount` の `c := uint(4) - 8` は gc でコンパイル不能な定数式（ファイル全体が minigo 専用実行なので存在できた）。B-3 修正で意図どおり trap するようになったため、同じ巨大 count を gc 合法な `^uint(3)` に書き換え — テストの意図（unsigned な count の生ビット列でシフト）を保持したまま。
- **自分が書いた新関数が「既存関数の再実装」だった**: B-2 用に `numericTypeName` を新設したが、直後の Devin Review 指摘（多段チェーン）を直す過程で `numericBasicName`（`builtinTypeName` から bool/string/error を除く既存セット）と `peelNamed`（多段 typedef 解決の既存経路）がそのものズバリ存在したと気づいた。→ `numericTypeName` を削除して `numericBasicName(basicNameOf(peelNamed(...)))` に置き換え。「この名前集合が欲しい」と思ったとき既存セットとの差が "error" だけだったこと、および名前解決の深さが peelNamed に既にあったこと — レビューの「再実装していないか」は自分の新コードにも向けるべきだった（§6.16 の ifaceOperand と同型の再発）。
- **Devin Review の2指摘は実ギャップだったが、それを直すと更に同根因の面が見えた**: `type A B` チェーン + `1e300 + c64` を修正中に、裸スカラー/GoValue オペランド側（`1.5 + intvar` → 9.5、`(1+2i) + v` → (9+2i)）がまだ同じ wrap 経路を残していることを確認。Named に限る修正はクラスの半分しか閉じないため `scalarOperandTypedef` でゲートを全数値オペランド形状に拡張した（`300 + a` が 36 ですらなく 308 を出していた — wrap すらしていない別ドメイン誤りだった、という副次観測も記録）。
- **trap 文言の class 一致を明示的に採用**: minigo の `constant 300 overflows int8` は gc の `300 (untyped int constant) overflows int8` と同じ reject クラスだが、文言規約は違う（`1.5` に対しても gc は "truncated"、こちらは "overflows"/"cannot use"）。文言一致は difffuzz の verdict クラス上「コンパイル拒否」として同格なので、同一クラス内の表記差は残す判断を明示する — 文言 fidelity が必要になったら別項目にする。
- **`1e308 * 2` のような bare-const overflow は既存挙動と判定**: stash して確認したところ、UConst 導入前から `uconstNative`/fmt 境界で RuntimeError としてパニクる — 本ラウンドの変更によるものではなく、境界クラス（定数ドメインの物質化不能）として今回もスコープ外に置いた。
- **「corpus sweep の1件」が想定より深かった**: CAP#10 の bug470（`float64(float32(0.01))`）は「corpus の小修整正」のつもりで拾ったが、convertConst の payload 丸めと intrinsics の fmt 幅レンダリングの2面に跨る修正になった。さらに Devin Review の B-4（tie 値の二重丸め）で同関数にもう一段踏み込んだ。小さい divergence が薄い層の下に厚い定数ドメインの問題を抱えている例。

### 残りの状況

- Stack #615 は 16本（CAP 修正10 + レビュー由来5 + 本レポート）。未マージ（マージはユーザー側）。
- 残件: deadlock 検出（スケジューラ機能、TODO `[ ]` のまま）、`inspect.Value` が const read で init() を起動する件、`reflect.StructTag.Get` 欠落、境界クラス記録群。corpus sweep / gen hunt の深掘り（バッチ数・depth 引き上げの飽和確認）は次ラウンド以降の余力案件。
- 文言 fidelity（"truncated" vs "overflows" 等）は同一 reject クラスとして扱う方針を上記のとおり明示した — 必要になれば別タスクで。

### 不備の振り返り（メモ）

- B-2 のコミットを B-3 と同じブランチに一度混ぜて push し、reset+force-push で剥がした — stack 運用では「今どのブランチにいるか」の確認を commit 前に毎回入れるルールが書かれていたのに漏れた。
- 子セッションのレビュー応答は2回に渡り転送が末尾切れした — 長い構造化回答を取るときは分割取得を最初から前提にする。
- `/tmp` のプローブは毎回単発で書き、stash/rebase 後の working tree に残らないようにした（今回は問題化しなかったが fuzzfix 書き換え判明が遅れた原因はテストスイート実行タイミング）。

## 6.18 実施ラウンド（round-16）: Stack #667 — difffuzz 残件掃討・修正7 PR・全体差分レビュー（バグ0件）+クリーンアップ

発端は round-15 同様、TODO.md の difffuzz 系残件を「1 root cause = 1 PR」で直列掃討する指示（CAP=10、基本ソロ・子セッションはレビュー委譲のみ）。成果: **Stack #667 に修正 7 PR（#652–#661、うち #657 はユーザー側挿入の panic-error-print）＋ レビュー由来クリーンアップ 1 PR ＋ 本レポート**。残キューが全て機能級・境界クラスに達したため CAP 未満（修正7件）で掃討を打ち切り、全体差分レビューを子セッション1件に委譲 → 返答をバグ優先→リファクタの順で判定・対応した。

### 実施内容

| フェーズ | 内容 | PR |
|------|------|-----|
| corpus sweep 補充 | `$GOROOT/test` 未走査サブディレクトリを difffuzz corpus モードで掃き、根因単位で pin | 各 PR の pin 付随 |
| difffuzz 修正 | blank label `_:` の重複宣言トラップ（#652）、`const s[0:i]` の UConst slice トラップ（#654）、`string(int)` 範囲外の U+FFFD（#656）、無名 literal/変換→名前付きスロットの declared tag 喪失（#658）、配列 slice bounds panic の "with length"（#659）、`recover()` の生 UConst payload（#660、corpus issue48898）、名前付き pointee 間ポインタ変換の read トラップ（#661、corpus issue56990） | [#652](https://github.com/podhmo/minigo/pull/652)–[#661](https://github.com/podhmo/minigo/pull/661) |
| 残件棚卸し | `dur_methods`・`geninfer_variadic_unify`・`geninfer_callee_t` は機能級 divergence として PENDING pin + TODO `[ ]` 記録に留め、issue8606b(unsafe)/issue13160(GC)/issue75764(tail-call スループット) は境界クラス、deadlock 検出は機能項目として据置 | 本 PR（pin + 帳簿） |
| 全体差分レビュー | 子セッション（swe-2-max、約35分）が main→先端の全差分を精査 — 差分内バグ **0件**、再実装疑義 0件、隣接の**既存** divergence 3件とリファクタ案5件を指摘 | （レビューのみ） |
| レビュー由来クリーンアップ | `tdShapeEq` の死んでいた face-spelling アーム除去、`sameTypeDef` の identity fast path、`runtime.Tag` への冗長 `Unwrap` 除去、`v.slice` の `int64(len(b.Elems))` ホイスト | クリーンアップ PR |
| レビュー由来バグ記録 | 指摘された3件の既存 divergence を再現確認した上で PENDING pin + TODO `[ ]`（実装に踏み込むと別根因級だったため。計画外の記録と判断を参照） | 本 PR（pin + 帳簿） |
| 本レポート | 本章 | 本 PR |

### レビュー指摘の判定結果

| 指摘 | 判定 | PR |
|------|------|-----|
| 差分内バグ | 該当なし（子レビュー verdict: 差分内バグ 0件） | — |
| B-adj-1 `(**uval2)(&pw)` が受理されて後でトラップ | **採用（記録のみ）** — 実機再現で `ElemOf(**uval2)` が `uval2` を返す（ポインタ一段潰れ）＋ `pointeeTag` が匿名 `*uval` セルの型を読めない、の二重機構と判明。pin `ptrconv_ptrptr` で記録 | 本 PR |
| B-adj-2 `panic((*int)(nil)); recover()` が `r != nil` false | **採用（記録のみ）** — Recover で `runtime.Tag` による Named 包囲を試したが `r != nil` の比較経路が Named を剥がして nil を見るため効かない。非 nil interface の boxing は現行の値形状では表現不能。pin `panic_typednil` で記録 | 本 PR |
| B-adj-3 `map[S]` キーが形状同じ別 named struct と衝突 | **採用（記録のみ）** — `map[A]int` への `B{1}` 書き込みを minigo が受理する compile-divergence として確認。pin `mapkey_namedstruct` で記録 | 本 PR |
| R-1 `tdShapeEq` の face-spelling アームは到達不能＋`Anon ?? Spec.Type` の12箇所オープンコード | **部分採用** — 死アームの除去のみ実施（`sa==nil && sb==nil` に畳み込み）。共通 helper `typeExprOf` の9サイト適用は churn 対効果で見送り（既存に `sigAnonOf`/`specTypeOf`/`arrayASTOf` の準 helper が散在しており命名統合が別論点になるため） | クリーンアップ PR |
| R-2 `runtime.Tag(et, runtime.Unwrap(dv))` の冗長 Unwrap、`sameTypeDef` の early-return 欠落 | **採用** — 両方適用 | クリーンアップ PR |
| R-3 `panicValue` の Named-composite アームが default と重複 | **不採用（今回）** — 指摘は妥当だが対象は stack 中の #657（ユーザー側 PR）のコードで、中間ブランチへの編集は restack 対象を広げる。別機会に送るのが適切 | — |
| R-4 `v.slice` の `v.arrayLen(f, b.Typ)`・`int64(len(b.Elems))` 重複計算 | **採用** — `n` ホイスト（arrayLen は別腕経路として維持、実際に二度評価していたのは len のみ） | クリーンアップ PR |
| R-5 `word` 文字列パラメータ → enum 化 | **不採用** — `sliceBoundsReason` の文言引数はエラーメッセージの局所表現で、enum 化は可読性を上げない | — |

### 計画外の記録と判断

計画時の仮説と実施後の理解のずれ、および計画に無かった事象への判断。

- **issue48898 の最小トリガは `type _ int` ではなかった**: TODO 記述では「blank 名 typedef」由来と読めたが、実際に最小化すると `panic(4); recover().(int)` 単独で再現した — 原因は recover が `*runtime.UConst` を生のまま返すことで、blank 名は引っかかったコーパスの表面に過ぎなかった。→ pin は最小トリガで作り直し、TODO の `[x]` 記述も「blank 名」ではなく「still-constant payload」側に書いた。コーパスソースの「付近にあった別の要素」に引っ張られて根因を誤読するパターンの再発（以前の round でも同型あり）。
- **`panic` の `any` 引数が materialize 境界という設計を明示的に採用**: `panic(4)` が定数ドメインのまま panic に届くのは、引数型 `any` への代入相当で gc では `int` に物質化される地点を通過したことを意味する。修正位置は「panic 内で物質化」か「recover で物質化」かの2択だったが、defer チェーンや未捕捉 panic の print 経路（`panic: 4`）が payload を直接読むのを考えると recover 側での物質化は最小爆破半径になる — panic 値本体は定数ドメインのまま保持し、recover() の interface 戻りだけ物質化する形に置いた。
- **ptrconv_named は1つの divergence に2つの機構が重なっていた**: `(*uval)(&u)` の read トラップは OpDeref の Named-pointer coerce が代入可能性チェックに落ちる件だが、`(*uint)(&w)` は別経路 — `convertPointer` が pointee が unnamed の時タグを付けず raw cell を返していたため、代入先で `*uval` として読まれていた。同じ pin の見た目の半分が別関数に起因していたため、re-tag 側と tag-stamp 側を分けて直した。
- **8件/CAP10 での打ち切り判断**: CAP 未消化のまま残キューを見たところ、`dur_methods`（`d.String()` すら無い — host facade の Duration メソッドセット実装が要件）・`geninfer_*`（共通 default 型 unification は inference 設計レベルの作業）は新規機能級、issue8606b/13160/75764 は unsafe・GC・スループットの境界クラスで、「1 root cause = 1 PR で潰す」対象の残りが尽きた。補充系（corpus sweep 実行済み・残りの corpus ソースは境界クラスに収斂）として次の divergence 採掘を続けても pin しか生えない判断を明示的に行った。
- **レビューの「隣接バグ」が全て記録側に落ちた判断の記録**: 指摘3件を精査したところ、全て差分ではなく**既存挙動**で、かつ修正が別根因級の深さだった（`ElemOf` のポインタ段潰れ＋`pointeeTag` の匿名ptr読み取り不能、`r != nil` の Named-unwrap 経路、map キーの shape 比較）。ユーザーの「バグフィックス全体が先」の意図はレビュー対象差分への直しなので、隣接の既存 divergence は TODO 運用ルール（タスクと無関係に見つけたバグは記録）に従い PENDING pin + `[ ]` 記録で留めた — この判定を採用するかはマージ前に確認されたい。
- **子レビューの「再実装チェック」は clean だった**: `callStringer` の `runtime.IfaceCallString` への移動は既存 helper の正しい共有、`panicComposite` は新しい軸で再実装ではないと verdict。自分が新設した `constPayload`/`materializeConstErr` 経路も重複疑義なし — §6.17 の numericTypeName 型の自己再実装は起きなかった。
- **`make format` の testdata 4ピン余分差分は継続発生**: rangearr_snapshot 等の 4 PENDING pin が goimports で触られる既知の癖で、コミット前に `git checkout` で戻す手順を継続した（round 間の持越し事項 — PENDING pin の gofmt 化か除外が別途候補）。

### 残りの状況

- Stack #667 は 12 本（修正7 + ユーザー #657 + クリーンアップ + レビュー後追修正2 + 本レポート）。全 open、マージはユーザー側。
- TODO 残件 `[ ]`: deadlock 検出（機能）、`dur_methods`・`geninfer_variadic_unify`・`geninfer_callee_t`（機能級）、`ptrconv_ptrptr`・`panic_typednil`・`mapkey_namedstruct`（本ラウンド新規記録、別根因級）、境界クラス記録群。
- corpus sweep の残り未走査・gen hunt の深掘り（バッチ・depth 増の飽和確認）は次ラウンド以降の案件として残置。

### 追記: レポート後の外部レビュー対応

本レポート作成後、別エージェントの全体差分レビュー（比較: main `b87ebf81` vs 先端 `c6931d6f`）で差分内回帰 2 件が指摘された。いずれも実機で再現確認し、回帰として修正 PR を積んだ（レビュー対応は CAP 外、pin は昇格済み＝PENDING なし）。

- **`ptrconv_store_tag`（[#665](https://github.com/podhmo/minigo/pull/665)）**: `*(*uint)(&w) = 7` が `var w W` の W タグを消し `w.M()` が `select M on cell of int64` で失敗。ptrconv_named(#661) で変換ポインタを `Named{*uint}` で包んだことで、setIndirect 内の古い `Unwrap` 経路（`PtrConvShared` の裸セル前提）を踏むようになっていた。ポインタの要素 typedef は値の検査にのみ使い、セルが既に持っている declared tag で格納し直す形に修正 — これは「read 側で re-tag する設計」（#661）と書き込み側の整合であり、旧コードが Unwrap を選んだ当時は read が coerce だったため tagged 格納が読めなかった事情があった。
- **`errret_alias`（[#666](https://github.com/podhmo/minigo/pull/666)）**: `func (E) Error() Text`（`type Text = string`）が `error` を満たさず `fmt.Println(E{})` が `{}` を出力。`isStringerFuncType` が結果 ident の綴りを `"string"` と文字列比較していたため。gc の signature identity に従い、package scope の `type X = Y` spec を辿って解決する実装に変更（defined type・`string` シャドーイング・alias 循環は正しく false 側に倒す）。

計画外の記録として: 子レビュー（差分内バグ 0件 verdict）では検出されなかった回帰が別レビューで見つかった — レビューは1回でなく複数観点で走ると抜けが減ることが今回示された形。また2件目の混入元はユーザー側 PR #657 の `isStringerFuncType` で、スタックへの混入レビュー対象を「自分の差分」だけに限定しない方が良いという知見になった。

### 不備の振り返り（メモ）
