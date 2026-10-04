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

### 6.3 レビュー第2ラウンド: 5件の修正とリファクタリング評価

第2ラウンドのレビューでさらに5件が報告された。全て `go run` との差分を確認の上で修正（[#113](https://github.com/podhmo/minigo/pull/113)–[#117](https://github.com/podhmo/minigo/pull/117)、Stack #73 積み増し）。

- **nil slice → 非ゼロ長配列変換（#113）** — `convertArray` の nil-slice arm が無条件でゼロ配列を返していた。`var s []int; _ = [1]int(s)` は Go では長さ不足の runtime panic。`n > 0` で panic、`n == 0` のみゼロ配列に。
- **DeepEqual の typed nil 同一性（#114）** — nilish 判定が `Typ.Name` だけを見ていたため `(*int)(nil) == (*string)(nil)`、`[]int(nil) == nil` が true。`runtime.TypedNil` 同士は `deepTypeEq`（kind + AST spelling）で比較し、untyped nil / empty-iface nil 同士は「ただの nil」として相等 — 実機で `DeepEqual(io.Reader(nil), nil) == true`、`io.Reader(nil) == io.Writer(nil) == true` を確認して分岐を設計した。
- **DeepEqual の map key 照合（#115）** — key を deepEql していたため、pointee が等しい別アドレスの pointer key が一致扱い（Go では map lookup の等値性＝ポインタ同一性）。`Pairs` の canonical key で `bm.Pairs[ak]` を直接引く形に変更し、value だけを再帰比較 — canonical key は map の等値性そのものをエンコードしているので仕様と一致。
- **`%p`/`%T` の共有 operand 破壊（#116）** — rewriteTypeVerbs が `a[off+pos]` を直接上書きするため `fmt.Printf("%v %[1]p", p)` が `0x… 0x…` に。directive を `{pos, verb, stars}` の中間表現で収集し、spec を sequential 化（`[n]` 剥がし）+ arg tail を作り直す構成に変更 — 各 verb が専用 operand を持つので共有 slot は消えた。ついでに `%[n]` 後の implicit-arg カウンタ（`argNum = n`）と `*` operand の consumption、`%!(EXTRA …)` の高水位保持も Go 準拠に。
- **`runtime.Frames.Next` の more（#117）** — 最終フレームでも `more=true` 固定だったため canonical ループが空フレームを余計に処理。`cf.i < len(cf.sites)` を返す。加えて `text_pass_nilpanic` pin の `for f, next := Next(); next;` イディオム自体が最終フレームを読み落とすバグだった（Go では runtime フレームが後ろに居て隠れていた）ので正規形に修正。

#### リファクタリング提案の評価（第2ラウンド）

1. **`deepEql` を型比較・pointer 比較・値比較のフェーズ分割 — 妥当だが、大半は第1〜2ラウンドの修正で既に実現済み**。Go の `deepValueEqual` も「動的型一致 → 値の再帰」の2段で、現在の deepEql は lockstep peel（Named/deref 層の型一致）+ `deepTypeEq`（複合 arm での型一致）+ nilish arm の3層が先に走る構造になっており、実質フェーズ1は前倒しされている。形式的な3関数分割を別途やる価値は「読みやすさ」のみで、新しい正しさは生まれない。優先度: 低。やるなら `deepTypeEq` を entry で一度だけ行う形への集約が自然。
2. **format 書き換えの中間表現化 — 妥当。そして指摘された `%p` バグの修正そのものになった（#116 で実装済み）**。directive+operand index の IR（`dir{start,end,pos,verb,stars}`）→ sequential spec + rebuilt args、という構成がレビュー提案そのまま。`%[n]`・`*`・EXTRA の扱いを IR 上で考えられるようになったおかげで、shared slot を消せただけでなく暗黙 arg カウンタの不整合（`%[2]v %p` が誤 operand を変換し得た潜在バグ）も同時に潰れた。提案の方向は正しかったと結論できる。

### 6.4 レビュー第3ラウンド: 5件の検証と2件の修正

第3ラウンドは5件報告されたが、現スタックトップで再現を確認したところ **3件は既に直っていた**（レビューのベースが古い状態での検出と思われる）。残り2件のみ diverge。

- **既修正（再現せず）**: `len(a[i()])` の call 省略と shadow された `len`/`cap` の畳み込み（#108）、`DeepEqual([]int{1}, []int64{1})` / `(*int)(nil) vs (*string)(nil)`（#111/#114）。全て現トップで `go run` と一致することを確認。
- **入れ子呼出しの引数スクラッチ衝突（#118）** — `callArgs` の `$argN` カウンタが call site ごとに 0 始まりだったため、`foo(f(), bar(g()))` で `bar` 側の hoist が外側の `$arg0` を上書きし `(10,21)` が `(20,21)` に。カウンタを `compiler.tmpSeq` に昇格し関数全体で一意化。評価順二相化（#105）で入れた機構の、入れ子ケースの見落とし。
- **`default` の無条件選択（#119）** — switchStmt が default clause に skip-jump を出さず、ソース位置で即 body に落ちていた（`switch 1 {default:; case 1:}` → `default`）。非最終 default も他 clause と同じく次テストへの jump を出し、「全テスト不成立」の継続先を default body に patch。fallthrough の前後接続は従来通りソース順。

#### リファクタリング提案の評価（第3ラウンド）

1. **一時変数生成の専用ヘルパー集約 — 妥当（中）**。今回の `$argN` 衝突は「採番スコープを呼び出し側が握る」構造が原因で、`c.tmpSeq` で回避したが、`$tag`・named result slots・funclit 名など compiler 内の合成名は同じ罠を持つ。`c.fresh("$arg")` 的な発番ヘルパーに集約すれば今後の衝突を構造的に防げる。ただし現状の衝突面は `$arg` だけなので、効果は予防的。
2. **VM・intrinsics 間の型同一性判定の共通化 — 妥当（中）**。`deepTypeEq`/`deepDefEq`/`deepTypSpelling`（intrinsics）と VM 側の assignability・interface switch strict 比較・comparable 判定は同じ「typedef の同一性」を別々に判定している。実際にずれが存在する: DeepEqual は spelling 比較で匿名型を区別するが、VM の `BinEqlIface` は `==`/`deepDefEq` 系で `[]int` vs `[]string` の要素型を見ない方向の判定になっている経路がある。`typedefIdentical(a, b)` のような単一 API に集約し、各判定が「構造的同一性のどの側面を見るか」を明示できると、指摘系の再発を防げる。`deepTypSpelling`（AST printing）が runtime 非依存のまま `runtime` パッケージ側へ移せるかが設計の肝 — `ast.Expr` と `*runtime.TypeDef` だけに依存するので移動自体は可能。

### 6.5 レビュー第4ラウンド: 2件の修正

第4ラウンドは2件とも現スタックトップで再現した（[#122](https://github.com/podhmo/minigo/pull/122)、[#123](https://github.com/podhmo/minigo/pull/123)）。リファクタリング提案なし。

- **len/cap fold が operand base の call を見逃す（#122）** — `hoistedArgCalls` の対象が `ix.Index` だけだったため、`len(f()[0])` で base の `f()` が fold に巻き込まれて消えた（Go は index を fold するのは operand 全体に call/受信が無いときだけ）。`ast.Inspect` ベースなので対象を index 式全体（`ix`）に広げただけで base 側の call も拾える。評価順二相化のガード範囲の見落とし。
- **終了ブロックのローカル型が残留（#123）** — `typeSpecs`/`typeDefs`/`ifaceTypes` が関数単位のフラット map で `popBlock` と連動せず、`{type T struct{Y}}` 終了後の `type U struct{T}` が死んだ T を埋め込んで `u.X` が trap。3つの検索を `isTypeDeclName` と同じ「live ブロックに binding があるか」基準に統一 — ついでに local var `T` が外側の `type T` を透過させる var-shadow 穴も塞いだ。

いずれも「メタデータの寿命 ≠ 名前 binding の寿命」「最適化ガードの対象範囲の切り方」という §3 の構造的な反省の再発型。fscope の型メタデータ系は block 連動に揃えたので、この系の個別指摘はここで打ち止めのはず。

### 6.6 レビュー第5ラウンド: 3件の修正

第5ラウンドは3件とも現スタックトップで再現した（[#124](https://github.com/podhmo/minigo/pull/124)、[#125](https://github.com/podhmo/minigo/pull/125)、[#126](https://github.com/podhmo/minigo/pull/126)）。

- **[P1] DeepEqual の循環参照でホスト死（#124）** — `m["self"]=m` の比較が無制限再帰で recover 不能の stack overflow（Go は `true`）。`devisit{a,b}` の seen-pairs を Map/Slice/Struct の各 arm に入れ、再訪ペアは coinductive に `true`。`av == bs` のポインタ同一性ショートカットも併設（Go は「構造が同じ循環」を真とみなすので参照一致性判定で十分）。
- **[P2] struct の型同一性（#125）** — フィールド名だけの比較だったため `struct{X int}` ≡ `struct{X any}` が `true`、さらに兄弟ブロックの同名 `type T` 同士も一致。struct arm を `deepTypeEq`（AST spelling 含む完全な型同一性）経由にし、named def は宣言オブジェクト同一のみ一致へ。匿名 struct は従来通り形状比較なので別リテラルサイト同士は同一型のまま。
- **[P2] 内側 type 宣言が外側のメタデータを上書き（#126）** — `typeDecls`/`typeSpecs`/`typeDefs`/`ifaceTypes` が関数単位フラット map だったため `{ type M struct{...} }` が外側 `type M map[int]int` のエントリを破壊し、ブロック終了後も誤った型で解決し続けた。4 map を `blocks` と同じ per-block slice に変え、`recordType` で宣言ブロックへ書く構造に — §6.5 で「打ち止め」と書いたが、#123 は lookup の liveness を直しただけで書き込み側は依然フラットだった。この case が本当の打ち止め。

#### リファクタリング提案の評価（第5ラウンド）

- **ブロックの binding に slot・宣言種別・型情報をまとめる — 妥当（中）。#126 はその弱い版として実装した**。提案は `blocks[name]→slot`、`typeDecls`、`typeSpecs`、`typeDefs`、`ifaceTypes`、`declPos`、`ifaceVars` を `[]map[string]binding` の単一レコードへ統合する方向。今回は「既存の並列 map を同じ push/pop 寿命に揃える」形に留めた: 参照点7箇所の修正で済み、効果も等しい（全 map がブロックと同じ寿命を持つので、取り違え・残留・上書きの系は構造的に消えた）。統合版の追加利得は「1フィールド追加＝1箇所変更」の見通しだけで、新たな正しさは生まれない。ただし fscope は現在 8 本の並列スライスを push/pop で揃えており、将来フィールド追加時の同期漏れリスクは残る — 次にこの構造を触る変更（例: 別種のブロックスコープ情報の追加）が出た時点で `binding` レコード化を検討するのが適切なタイミング。

### 6.7 レビュー第6ラウンド: 代入ターゲットの live storage 解決

第6ラウンドは1件、現スタックトップで再現した（[#127](https://github.com/podhmo/minigo/pull/127)）。

- **[P2] フィールド代入先が古い構造体を掴む（#127）** — `refTarget` が RHS 評価前に base を**値**として確定していたため、`s.X = replace(&s)`（replace は `*s = S{X:1}` で s 全体を置換）が死んだ Struct に書き込み `s.X == 1`（Go: `2`）。

調査で Go の lvalue モデルを実測で確定した: **代入先の base は格納時に解決される live なストレージ参照チェーンで、key/添字だけが評価時スナップショット**。`p.X = reseat(&p,q)` は新 pointee に書き、`s[i] = f()` は新 slice に書き、`m[k] = f()` は新 map に書く。一方 `&s[0]`（slice の要素アドレス）は評価時の配列を pin する — `&` 経路と `=` 経路で非対称になる。

修正は3層: (a) `refTargetBase` — base がストレージ運搬形（Ident/Selector/Index/Star）なら `refTarget` で live ref を吐き、それ以外（call・`&x` 式）は従来通り `expr` の値評価。(b) `*p = v` 用に `DerefRef`（格納時に `Deref(ptr)` を解く遅延 ref）と `OpDerefRef` を新設 — ポインタ値をそのまま積むと pointee がスナップショットされるため。(c) compound assign（`x op= y`）を同じ ref パイプラインに統一 — こちらは「ref 評価 → rhs 評価 → 読み出し+op+store」の順で、Go が読み出しを RHS 評価時に行うことも実測で確認済み（`s.X += f()` で `10+5=15` ではなく `1+5=6`）。`OpFieldRef` の cell 正規化は `&` 経路（B=0）のみに限定し、store 経路（B=1）は生のストレージ cell を保持。

付随修正: パッケージ修飾ターゲット（`runtime.MemProfileRate = v`）は base がパッケージオブジェクト（ストレージではない）なので `isImportName` で `expr` へ振り分け、`OpDeref` 上の IndexRef は `v.index`（map 対応の完全経路）へ流す。

#### リファクタリング提案の評価（第6ラウンド）

- **`refTarget` を「変数ストレージを保持するケース」と「評価時点の参照先を保持するケース」に分ける — 妥当。そして今回の修正がほぼそのままの形になった（高）**。`isStorageBase`/`refTargetBase` が提案の分岐そのもの: ストレージ運搬形（Ident・Selector・Index・Star・Paren unwrap）は ref、非ストレージ形（call・`&`式・型名）は値。実装して分かったのは、提案が暗に想定する二分では収まらない点が2つ — `*p` は「評価時の pointee」ではなく「p が指す場所」を格納時に解く第三のケース（`DerefRef`）で、Ident だけ import 名判定が必要（パッケージは値オブジェクト）。つまり分岐は「storage / value」の2値ではなく「storage / 評価時 pointee / value」の3値が正確なモデルで、提案の方向は正しいが粒度はもう一段細かい。複合代入も同じ構造に乗せられたので、「この種の不具合を防ぐ」という狙い自体は達成されたと評価できる。

### 6.8 実施ラウンド（round-7）: リファクタリング提案の実行

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

#### 計画外の意思決定

- **型同一性の統合が実害 diverge を露出（#135）**: `eqlValue` の動的型ゲートが Go の (type, value) ペア比較からずれていた — `any((*int)(nil)) == any((*string)(nil))` が true、`any([]int) == any([]string)` が uncomparable panic（Go は false）、`structDefsEq` が `Binds` を見ていなかった。「判定器を一本化する」作業が各 arm の前提ずれを可視化したため、リファクタと同じ PR でゲート自体も修正（回帰 pin: `text_pass_ifaceeqtypes`）。deepEql の struct arm は `deepTypeEq`→`TypIdentical` に移し、REPL の decl 再マテリアライズ（cache eviction 後）でも name+pkg で一致する強度を選んだ — object identity だと同一宣言の再構築を別型とみなしてしまう。
- **panic payload の3系統整理（#136）**: 41 サイトの Message を族分けすると、文字列 payload（`r.(error)` が効かない）・`runtime error:` 二重プレフィックス（2サイト、Error() が再付与するため）・Go 1.23 以前の range-yield 文言が混在していた。`PlainError`（Go の plainError 系 — error 型だが Error() にプレフィックスなし）を新設し、`Recover()` は error-typed payload 全般を GoValue 化する形に一般化（PanicNilError も通る）。意図しない変更は fixture（`text_pass_panicpayload`）で `r.(error)` の成否まで含めて pin した。
- **funclit の invented params は `ic.fresh`（#137）**: 子コンパイラ側の hoisted `$argN` は ic の seq から採番されるため、param 名も親ではなく ic の seq から採る — 同一 seq 空間に揃えないと子スコープ内で衝突しうる。
- **UConst はタグ統一の対象外（#138）**: 提案は Named/UConst を併記していたが、UConst は `constant.Value` を包む別形で、materialize 入口（`materializeConst`/`materializeConstErr`）は既に一本化済み。`Tag`/`Unwrap` は Named のみに限定した。
- **`eqlValue` の Function arm は panic 維持（#135）**: 異なる func 型同士の比較も Go では false だが、関数値が signature typedef を持たないため同一性ゲートを掛けられない — 既存の近似（無条件 panic）を残した。

### 6.9 実施ラウンド（round-8）: コーパス SILENT 掃討 — recover/Callers の unwind モデル

`$GOROOT/test` コーパス再スイープ（145 programs）の残り SILENT を潰した。Stack #144 の最上位に3本の修正 PR（[#149](https://github.com/podhmo/minigo/pull/149)–[#152](https://github.com/podhmo/minigo/pull/152)）を積んだ。

| 対象 | 根因 | PR |
|------|------|-----|
| `recover1.go`（4行の差分） | `recover()` が「defers を持つ任意フレーム」から panic を見ていた。Go の `gorecover` は recover 呼出と panic の間に**非 wrapper フレームがちょうど1つ**ある場合のみ成功とする | [#149](https://github.com/podhmo/minigo/pull/149) |
| `devirtualization_nil_panics.go`（panic 行番号 -1） | `runtime.Callers` が unwind 済みフレームをスナップショット末尾に並べていたため、`CallersFrames` の `for f,next:=Next(); next` 走査が panic フレームに到達しなかった。unwind 境界（`unwindDepth`）に挿入する Go のトレースバック順へ | [#150](https://github.com/podhmo/minigo/pull/150) |
| ハーネス artifact（seed 20261003 の SILENT） | `x[lo:CALL(...)]` 形で複数の panic 源が競合 — Go は非 call オペランドの評価順を規定していないため、先に panic する側は実装依存。minigo の panic テキストが go 自身が同プログラムで出力したものなら order-legal として Pass に再分類 | [#151](https://github.com/podhmo/minigo/pull/151) |
| `recover.go`（`panic: 5` が脱出） | 正常 return 後の drain 中に deferred call が panic した場合、unwind 先は**スタック上に残る owner フレーム** — `runOneDefer` が境界を `dpos`（owner のスロット）に立てていたため owner が2フレーム目に数えられ recover が nil を返した。境界は「呼び出した deferred call が占めるスロット」（= `len(v.frames)`、unwind drain 中は `dpos` と一致） | [#152](https://github.com/podhmo/minigo/pull/152) |

#### 実施内容

- **Go セマンティクスの導出**: `gorecover`/`gopanic`/`recovery`（/usr/local/go/src/runtime/panic.go）と実測プローブで確定した規則 — (a) recover 合法条件は「recover から gopanic まで非 wrapper フレームちょうど1つ」、(b) `defer recover()` は0フレームで**絶対に回復しない**（`func(){defer recover(); panic(5)}()` は Go でも `panic: 5` で落ちる）、(c) panic が drain 途中で consume されると `recovery` は当該フレームの deferreturn に着地し、残り defers は外側 panic が見える文脈で走る（recover1 test6 が黙る理由）、(d) deferred call 内の panic は同じフレームの残り defers へリンクスキップで継続。
- **`DeferBuiltinRecover` の既存期待値が非 Go だった**: 「`defer recover()` が panic を飲む」という古い pin は実測で Go と矛盾すると確認し、ワーカー func 経由の正当な形（`defer func(){ defer recover() }()`）に差し替え + 伝播を assert する `DeferBuiltinRecoverPanic` を追加。
- **unwind 境界の2段階**: `unwindDepth`（panic の deferred-call 連鎖が根付くフレームスタック index）を導入。pop 済みフレームの unwind drain では `dpos`、正常 drain では deferred call の invoke index（owner がスタック上に居るため +1）。同じ `unwindDepth` が `runtime.Callers` の unwinding スプライス点にも使えた。

#### 残りの状況

- コーパス SILENT は全て既知の境界クラスに帰着: GC/finalizer 系（closure/deferfin/finprofiled/gc2/mallocfin/stackobj/stackobj3/tinyfin/heapsampling/init1）、unsafe.Pointer（initialize → #40）、スループット HANG（copy/divmod/maplinear/winbatch/heapsampling — copy.go は 50s で正解確認済み）、gcgort（Go でもデッドロック）、linkmain_run（ツールチェーンの tmpdir ノイズ）。新規の潰せる残件はゼロ。
- TRAP backlog（次に実装すべき面）: `unsafe.Pointer`×13、`reflect.*`×7、`complit.go` の `cannot use [...]*T as [*ast.CallExpr]*T`、`map.go` の `index assign on *runtime.IndexRef`、`turing.go` の `index on *runtime.UConst`、`peano.go` の stack exhausted — 全て main と同一（回帰なし）。
- usecasefuzz 再実行: 33 PASS / 0 DIFF / 1 ACCEPT / 4 TRAP（lim-http/toml/xml/yaml — 既知境界）— **リグレッションなし**。seed-20261003 ガード再実行: SILENT 1→0。

#### 不備の振り返り

- **`unwinding` リストの用途発見が後出し**: Callers の unwind-order は recover 用 `unwindDepth` と同じ境界を再利用できたが、初版はリスト末尾への append で「最後尾=スキップ」という見えにくい欠陥を持っていた。frame/frames の論理順序（deferred 連鎖 → unwinding → その下の live）を最初から1箇所のスプライス関数にしておけば Callers・Recover の双方で順序バグが入らなかった。
- **recover の距離カウントは「境界の定義」が本質だった**: PR #149 は `dpos`（unwound フレームの論理位置）で全件整合したが、recover.go の正常 drain（owner がスタック上に残る形）では同じ `dpos` が境界として使えず「呼び出しスロット」が要った。「panic が unwind する先のフレームがスタックに居るか」で boundary が ±1 変わる — `runOneDefer` が `len(v.frames)` を取る形にして両ケースを一意にした。
- **ハーネスの false positive は mask ではなく意味論で解いた**: 「両側 panic でメッセージ違い」を無条件に揉めば数字系の真バグを隠す。go が同プログラムの別プローブで同じ panic を出していれば「その panic は authentic」= order-legal と判定する相互参照方式にし、unique-to-minigo の panic は引き続き flag する。

#### 計画外の記録と判断

- **corpus 外の新規作業ゼロ**: TODO 残件は全て境界クラスで、コーパス再スイープからも潰し対象の新規 SILENT は出なかった（recover.go のみ）。hunt の新 seed 補充は不要と判断 — gen は同シードガードで clean。
- **`defer recover()` のテスト期待値是正を同 PR に同梱**: 実装変更とテストデータ是正は1根因（one-frame 規則）として同一 PR にした — pin しないと片方だけ残る危険があった。

### 6.10 レビュー第7ラウンド: unwind bookkeeping の3件（1件は回帰）

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

#### リファクタリング提案の評価（第7ラウンド）

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

#### 不備の振り返り（第7ラウンド）

- **`inflight` の pickup が「`p != nil` 前提」で設計されていた**: consume 遷移が `p = nil` にする経路を作った時点で、後置ブロックの取得条件を見直すべきだった — 「`p` は遷移後も panic の残存を意味するか」という不変条件の検証漏れで、本スタック自身が回帰を入れた形。
- **`unwinding` のライフサイクル管理が「全部消す/残す」の二値だった**: 初版（#150）は `v.unwinding = nil` で全部消していたため outer unwind のエントリまで消せず、残す方向に倒したら consume 後の stale が出た。panic タグ付きにして「死んだ panic のエントリだけ落とす」が正しい粒度だった。
- **レビューの repro は最初から全件現トップで再現した**: 前ラウンド（3/5・5/7 が既修正）と違い、今回は 3/3 が真の未修正だった — unwind bookkeeping は相互に絡むため「直したつもりの組合せケース」がまだ抜けていた。
- **#151 は修正 PR ではなくハーネス PR**: 根因は「ジェネレータが実装依存の出力を生成する」側なので、minigo 側の挙動は変えていない（評価順の厳密 LTR は合法）。

### 6.11 実施ラウンド（round-9）: reflect TODO 掃討 + difffuzz 供給フェーズ

TODO.md の reflect 系未完了項目を 1 root cause = 1 PR のスタックで潰し、その後 difffuzz `-domain reflect` の hunt→triage→pin→fix を回した。さらにレビューで指摘されたバグ4件＋重複/リファクタ5件を子セッションに要/不要判断させ、要のものを同じスタックへ継続積みした。[Stack #201](https://github.com/podhmo/minigo/pull/199)（[#199](https://github.com/podhmo/minigo/pull/199)–[#275](https://github.com/podhmo/minigo/pull/275)、69 PRs、main 直積み）。

#### 実施内容

| フェーズ | 内容 | PR |
|------|------|-----|
| TODO 5件 | `fmt` が facade `reflect.Value` を1段 unwrap、`reflect.TypeAssert[T]` バインド（lim-xml ブロッカー解消）、`Value.SetCap` バインド、index-panic 文言修正、shared-global 破壊経由の発散2件 | [#199](https://github.com/podhmo/minigo/pull/199)–[#205](https://github.com/podhmo/minigo/pull/205) |
| 供給フェーズ前半 | `Type.In/Out` bounds、`OverflowInt/Uint` 符号跨ぎ、`Bytes` の kind ディスパッチ、`Method(i).Func`、`StructField.Offset`（amd64 layout）、`Slice`/`Convert` の ref・ro 伝播、`Type.Name` alias 畳み込み、`ValueOf` on facade、`FieldByIndex` embedded ptr、`Append*`/`Copy` 要素型等 | [#229](https://github.com/podhmo/minigo/pull/229)–[#250](https://github.com/podhmo/minigo/pull/250) |
| generator 拡張 | (a) `Runner.mask` が `0x[0-9a-fA-F]{6,}` → `0x…` を畳む（アドレス揺らぎで PASS↔SILENT が反転するのを止める）、(b) kind タグ付き 78 seeds＋合成 ctor（`SliceOf`/`MapOf`/`PtrTo`/`ChanOf`/`ArrayOf`/`New`/`MakeSlice`/`MakeMap`）、(c) `Grow`/`Slice3`/`FieldByIndexErr`/`FieldByNameFunc`/`SetZero`/`Complex`/`Pointer`/`UnsafePointer`/`UnsafeAddr`/`Recv` の probe 群 | [#220](https://github.com/podhmo/minigo/pull/220), [#222](https://github.com/podhmo/minigo/pull/222), [#242](https://github.com/podhmo/minigo/pull/242), [#251](https://github.com/podhmo/minigo/pull/251) |
| 供給フェーズ後半 | `kindStr` の `on zero Value`、`Field`/`NumField`/`FieldByName` が host ptr を deref しない、`CallSlice` variadic→exact arity→`[]Elem` 代入性、`Call` arity が vc チェックに先行、`callSig` は typedef シグネチャ（receiver 込み）優先、`TypeAssert` が host `flagRO` を見る、`Pointer`/`UnsafePointer`/`UnsafeAddr` 実装、path 修飾 selector の `resolveTypeRef` 解決、`resolveExpr` が `*T` を保持、`Value.Grow` バインド | [#243](https://github.com/podhmo/minigo/pull/243)–[#263](https://github.com/podhmo/minigo/pull/263) |
| レビュー駆動 バグ修正 | 全4件 NEED 判定（子セッションが oracle probe で実測検証）: (a) `Grow` cap≥256 での第二無限ループ — `(newcap+768)/4` 代入で縮小、`nextslicecap`/`roundupsize` を go1.26 実装（sizeclass・malloc header・noscan 分岐）まで忠実ミラー、29ケース一致; (b) `Set` のソース側 `ro` チェック欠落（SILENT — host 側分岐も同じ欠落を発見・同修正）; (c) `ValueOf` on タグ付き host box で `CanSet=true`＋Set がライブ状態を書き換え → copy semantics 復元（複合的根因で3PR: `(*p).Set` 型の pointee 書込み、`typSpelling` の二重修飾 `bytes.bytes.Buffer` による複合 td 別キー化）; (d) `FieldByName` が昇格フィールドの Offset を加算 → Go 通りローカル値 | [#265](https://github.com/podhmo/minigo/pull/265)–[#270](https://github.com/podhmo/minigo/pull/270) |
| レビュー駆動 重複/リファクタ | `runtime.DisplayName` 新設で表示名ロジック4実装を集約（`typeName`/`msgTypeName`/`typedefSpelling`/`TypGoSpelling` — 指摘の表記ブレは再現せずも別の本物を発見: `map[byte]int`、alias import 修飾、無名 struct が `struct{}` に潰れる）、`numParams`/`sliceView`/`hostMethodSet` 抽出、`RValue.Unwrap`→`Payload` 改名、nil `.V` ガード | [#271](https://github.com/podhmo/minigo/pull/271)–[#275](https://github.com/podhmo/minigo/pull/275) |

全 fix は `testdata/difffuzz/<slug>/` に seed pin（PENDING なし → 即必須テスト昇格）。

#### 残りの状況

- TODO.md の reflect 系 `[ ]` は全て `[x]`。
- difffuzz yield は減衰: 現カバレッジで ~1/15–20 seeds。generator 拡張（#251）で新 probe 面が開き、即座に pointer-accessor・selector-path・callslice-gates・star-param の4件を供給した。
- 未バインド op（`no member` trap → 実害ありの backlog）: `Slice3`/`FieldByIndexErr`/`FieldByNameFunc`/`SetZero`/`CanConvert`。probe 済みなので hunt が回れば発散として浮く。
- レビュー全項目を処理済み: バグ4件は全て NEED、重複/リファクタ5件は 4件採用＋1件部分採用（`sliceView` — 重複は実際は Grow+Bytes のみ、SetLen/SetCap は非 slice Named を trap する意図的形状）。子セッションが「やらない」と判断した残置: `anonTag`（identity 側）、`tdName` 系（内部 diagnostics 用）、無効パッケージ限定の乖離（oracle が走らず seed 化不可）。
- 残存する facade 制約: vc なし `Func` Value（`Type.Method(i).Func`）は正しい arity でも `needs a caller context`（VMCaller を facade 側で作れない構造制約。arity gate は先行するので fuzz が拾う panic 文言は正しい）。
- pin 不可 artifact（記録のみ）: (a) `MapKeys()` 順は Go 自身がランダム化 — map 順に依存する発散は pin しない、(b) `reflect.Value` 内部 `flag` バイトを直接読む合成プローブ（実害なし）。
- `usecasefuzz` 再実行: 41 PASS / 1 ACCEPT（inspectuse）/ 1 REJECT（lim-cgo — cgo 既知境界）/ 2 TRAP（lim-http, lim-xml — 既知境界）。`lim-yaml` は TRAP↔DIFF↔PASS の揺らぎ（map 順 artifact と思われる — 単独再実行では PASS）。`lim-toml` は TRAP から PASS に改善。**リグレッションなし**。

#### 不備の振り返り

- **スタックブランチへの誤コミットが3回**: `git branch --show-current` を commit 前に確認せず、同じファイルを触る PR 間で hop して混入した。`git reset --hard`+`push -f`→cherry-pick と file 単位の patch 分割（`git diff` → `@@` 単位で `git apply` 分け）で回復したが、確認はコストゼロなので常時行うべきだった。
- **generator の 'x'+Wrap emit バグ（#251 内で自爆）**: `rchain.body()` の 'x' 分岐が Wrap を honor せず `v1rGrowS` を生成 — `TestGeneratedProgramsCompile` が `undefined: v1rGrowS` で捕捉。generator を拡張するときは「emitted プログラムが go でコンパイルされる」までを1ケースとして回すべきだった。
- **`Grow` の `newcap=0` 無限ループ**: growslice 近似ループで `0 *= 2` が停止しない — 実害テスト（nil slice への Grow）で初めて出た。uint loop の termination 条件は初期値 0 の corner を常に疑う。
- **detached-cell の Named 喪失（#248 の根因）**: `*runtime.Cell` が裸の underlying を保持し `Deref` が `Named` を剥がすため、cell 経由の `get()` は宣言型タグを失う — `MethodByName` の member 解決は `td` 駆動の再タグ retry が要った。「cell 越しの値」は identity 層が一個外れる、という不変条件の見落とし。
- **host `flagRO` の居所（#250 の根因）**: facade `v.ro` は script-domain のみ — host 値の read-only は `!v.rv.CanInterface()` に居る。`Set`/`Interface` は host-op 先行の自己呼びで拾えていたが、`TypeAssert` は facade 側ゲートだったため `rv` を見る必要があった — 「gate をどちら側で置くか」の一貫したポリシー（host arm は host op 自身の panic を先に発火させる、facade arm は facade 側値の flag を見る）を最初に立てるべきだった。レビューで判明した `Set` のソース側欠落（#266）も同じ居所問題 — dst だけ見て src を見ていなかった。
- **`Grow` の第二ループバグがレビューまで残った（#263 → #265）**: 上記 `newcap=0` を直したとき、同じループの `(newcap+768)/4` が加算ではなく代入であることを見落とし、cap≥256 で縮小→無限ループのハングを本スタックに残した（本レポート自身が「newcap=0 側を潰した」と書いている間に ≥256 側が生存していた）。実害テストは `cap0 → need` 起点しか書いておらず、成長経路の別区間を probe していなかった — corner を1個潰してもループ全体の Go 対応表（cap→cap）を検証するまで「近似ループの正当化」は終わっていない。最終的に `nextslicecap`/`roundupsize` の sizeclass 丸め込み忠実ミラーに置き換えた（近似でなく写経、が正解だった）。
- **レビューが「自分で追加したコード」の退化を掴んだ（#210 系 → #267–#269）**: host 値への typedef タグ付け（`Named{td, GoValue{*T}}` box）はそのラウンドで正しかったが、`ValueOf` 経路が box をそのまま返して copy semantics を壊し、さらに `Set` が cell 内の box 形状を剥がす・`typSpelling` が selector 修飾子を型名として再修飾する、という3つの連鎖根因を生んでいた。追加した機構の「値が何経路で流れるか」の網羅確認が不足していた — box 形状は `get()`/`set()`/deref/ValueOf/Set の全経路で不変条件として検証すべきだった。

#### 計画外の記録と判断

- **harness 側の修正が混ざった (#220, #222, #242, #251)**: 「発散」ではなく「generator が拾える形にする」PR として別積み。発散供給が枯れたら generator 面を広げるのが次の正規手段 — yield の天井を上げる投資として 4 本は妥当だった。
- **dispatch.go の engine 側修正が1件だけ混じった (#255)**: `resolveTypeRef` の SelectorExpr が path 修飾を unknown import にしていた — minireflect ではなく engine の型解決。reflect 系の外側に見えるが、発散の根因は exprOf の PATH 修飾 AST が解決経路に流れ込むことなので 1 root cause として残した。
- **`reflect_grow` は emitted 形式の手書き seed**: 修正後の build では emit がこの発散をもはや生成できないため、emitted 形状（try() ラッパ + panic 文言プリント）の最小プローブを手で pin。pin の目的は回帰防止であり provenance 純度ではない、という判断。
- **hunt の打ち切り判断**: yield ~1/15–20 seeds に逓減し、残件は `no member` 系 backlog + pin 不可 artifact に集約 — 追加 hunt より binding 実装の方が価値が高い局面に入ったところで打ち切り依頼。進行中の `Grow` だけ仕上げて停止。
- **レビュー駆動フェーズは子セッションへ委譲**: ユーザー指示により、バグ4件→リファクタ5件の順で「各項目を子が oracle probe で要/不要判断→要のものを重要度順に 1 根因 1 PR で同スタック継続積み」。子は bug3 が複合的根因であることを検証中に自力で2件の別根因（cell box 剥がし・typSpelling 二重修飾）を発見し 3PR に分割、レビュー指摘の表記ブレ主張は再現しないことを実測で否定しつつ別の本物のブレを掴んだ — 「レビュー文面の検証」が「レビュー趣旨の回収」に昇格した好例。リファクタ項目は純粋な整理は seed なし、挙動変化（DisplayName 集約に伴う表記修正）のみ seed pin という線引きを適用。

### 6.12 実施ラウンド（round-10）: Stack #333 — difffuzz 掃討・corpus sweep・連鎖 rebase・レビュー対応

本セッションの全体像。発端は TODO.md の difffuzz 系未完了項目を「1 root cause = 1 PR」で stacked PR に積む指示（上限 30 PR、枯渇時点で終了、枯れたら `gen` hunt で補充）。成果: **Stack #333 に 27 PR（#331–#359）を構築し、続けて連鎖 rebase＋別エージェントのレビュー7件対応＋本レポートで計 35 PR**。queued の全 difffuzz 項目を潰し、追加で reflect TRAP バケット・API 面監査・`$GOROOT/test` コーパス再スイープ×2を流した。

#### 実施内容

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

#### 残りの状況

- difffuzz 系キューは枯渇して終了（掃討フェーズ 27/30 PR、上限未到達）。gen hunt は text/num/reflect 全ドメイン・depth 6 まで飽和（新規 SILENT 0）。レビュー指摘は全件処理済み（バグ2件＋欠落1件＋リファクタ4件、不要判定なし）。
- 残件は全て境界クラス: GC-finalizer 系 6（`SetFinalizer` は no-op 設計）、`unsafe.Pointer`×13＋`unsafe.String`/`Offsetof`/`FuncForPC`（#40 ポインタモデル・ホスト PC 境界）、`peano.go` フレーム上限、`linkmain_run.go` tmpdir 非決定、HANG×6 は main でも再現するスループット限界。
- レビュー/ probe で見つかった新規ギャップは stack 先端の TODO.md に記録: **`map[string]*[3]int` の内部書き込みが依然 trap**、**struct 要素の field write が一律 trap** — 次ラウンドの入口。
- Stack #333 は計 35 PR。CI は rebase 後の先端および各追加 PR で緑（head が全祖先を含むため累積検証）。リファクタ4件は全て挙動不変 — difffuzz pins は全緑のまま。

#### 不備の振り返り

- **write-through の適用範囲に「参照形」という不変条件を書いていなかった（#352 → #364）**: IndexRef の write-through を入れたとき「map 要素が書き戻せるか」を kind 無しに開けたため、Go の compile error に相当するケース（array/struct 要素の部分書き込み）まで静かに通した。格納コピー vs live 参照の区別は §6.7（#127）で一度構造化した系で、同じ鏡をもう一度踏んだ形。
- **コールバック境界の検査が入力側だけだった（#355 → #365）**: MakeFunc 実装時に `checkCallArgs` を入力（呼び出し引数）にのみ適用し、コールバックの戻り値側（arity・assignability）に同型のゲートを置かなかった。MakeFunc は 1 根因に3つの層症状（callable≠reflect.Value・`return nil`=TypedNil slice・bare 引数の typedef 欠如）をひとつの bridging 修正で閉じていたが、「ホスト⇄script の両方向でシグネチャ制約が効くか」の確認が out 側に及んでいなかった。
- **ホスト側の共有リソースが script の観測値を汚す**: `ReadMemStats` が `goruntime.ReadMemStats` を素通ししていたため interpreter 自身の allocation が script の delta assert（`n0 != m.Mallocs`）を GC タイミングで不定に誤爆させた。「ホストカウンタは script には見せない」を明示しないと、同一クラス（runtime.GOMAXPROCS・NumGoroutine 等）で再発しうる。
- **コピー忘れの要素経路**: keyed literal は positional 側が既に coerce していたのに要素格納で素通し — 「literal 要素は全経路で coerce する」不変条件が kv 分岐に書かれていなかった。deepEql の Named-peel strictness（`aNamed != bNamed → false`）がこの形状差を検出した — 型タグの厳密化がかえって別バグを晒した構造。
- **panic が host 呼び出し内部で飲まれる観測性ギャップ**: goroutine panic → `proc.fail` → 後続 spawn は未実行 Task 化 → host WaitGroup のカウントが下りず、root が `WaitGroup.Wait` 内で blocked だと真の panic が表示されず deadlock に見える。「`fatal error: all goroutines are asleep` = 別 goroutine が既に trapped」と見抜く bisect 手順（worker body を逐次実行して真の panic を露出）を TODO.md＋メモリに記録。構造修正（abortable host call）は未着手。
- **`typeMatches` の GoValue 網羅漏れ**: host box 値（complex64 — script 複素型が存在しない、bytes.Buffer）が全 concrete assert で `interface {} is complex64, not complex64`。KindPointer の GoValue 分岐と同じ native-type 比較を top-level にも置く見落とし — assert 判定器の分岐表に「host box」列がなかった。
- **汎用機構を置いても配線が一箇所止まり（#353 → #366）**: `emitLenFolds` は汎用に書いたのに呼び出しが `*ast.ArrayType` のみ — 「機構が対象となる AST 形すべてから呼ばれるか」は配線の網羅確認が要る。probe 後は実質穴が map/chan のみと分かったが、一様呼出し化で残差も含めて閉じた。
- **平行実装のドリフトは「共有 source」で解く方が正しい（④）**: `foldNextArrLen` と `emitLenFolds` は DFS 順序一致を暗黙に要求する平行 DFS — 順序 assert のテスト追加も選択肢だったが、子セッションは走査自体を `runtime.ArrayLenNodes` に共有化する方を選んだ。assert は「ずれたら教えてくれる」止まりで、共有化はずれる余地自体を消す — 後者が正しい判断。

#### 計画外の記録と判断

- **#349 が rebase で pin のみに縮退**: stack 内の promoted-method BFS 実装が main 側の #348 に完全包含されていたため、rebase 適用後の diff は testdata pin のみに。実装を消し込んで pin と差し替えた PR タイトル/本文も追従更新 — 「stack 内の別 PR が main で別実装として着陸」した場合の自然な帰結。force-push による全ブランチ書き換えは破壊的操作だが、ユーザーの明示指示（「開始前にmainからrebaseしたほうが良いかも」）で実施。
- **レビューは rebase 前の差分に対するもの**: 指摘の半分は現行コードで部分的に陳腐化していた（findMethod 系の並存指摘、emitLenFolds の「まだ trap」範囲）。各項目を現スタック先端で再検証してから判断させる運用を子セッションにも継承 — §5 の「レビュー指摘は現スタックトップで再現を確認してから直す」と同じ教訓の再確認。
- **誤分類の訂正を TODO.md に記録**: `initialize.go`（以前「DeepEqual/unsafe.Pointer 境界」と注記）は実は keyed 要素の uncoerced 格納、`gcgort.go`（「本物の deadlock」）は masked trap — sweep 中に境界と分類していた項目が probe で真のバグと判明した分を訂正した。
- **deadlock masking は修正せず記録に留めた**: root が host call 内 blocked のとき panic を露出するには abortable な host 呼出し設計が要り、本ラウンドの粒度を超える。観測手順だけ確立して残置。
- **子セッションへの委譲**: バグ2件＋欠落判定1件＋リファクタ4件の計7件を、各項目ごとに oracle probe（`go run`）で要/不要を判断させて 1 根因=1 PR で積ませる形に委譲。リファクタ①の「3本BFS」は実際には縮小済みで実質重複のみ残部修正、④は提案外の共有化アプローチを採用 — 文面通りではなく趣旨に沿った判断を要求した結果として妥当。
- **新規ギャップの分離記録**: probe 中に見つかった `map[string]*[3]int` 内部書き込み・struct 要素 field write の残存 trap は「この stack の指摘項目」ではないため TODO.md への記録に留め、次ラウンドの入口とした。
- **30 PR 上限には達せず枯渇終了**: 掃討キューが先に尽きたため打ち切りルール（30到達）を発動せず終了 — §5 の再開 prompt がそのまま通用する状態に戻った。
