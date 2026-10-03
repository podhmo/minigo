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
| **回帰**: mid-drain recover 後の deferred call が panic すると新 panic が飲まれる — `recovered: P` の後 `f returned` / `outer: <nil>` で E2 が消失。さらに `v.inflight` に残り、無関係な後続 `recover()` が死んだ panic を拾う（`h sees: E2`） | drain 終了時の outcome 取得は「元 panic `p != nil` の時だけ `v.inflight` を拾う」形だった。consume 遷移が `p = nil` にした後で later deferred が raise した panic は誰も拾わない | [#154](https://github.com/podhmo/minigo/pull/154) — 条件を `p != nil \|\| (inflight != saved && inflight != nil)` に統一 |
| `runtime.Callers` が consume 後に owner フレームを二重表示（live + stale unwound） | consume 遷移で `v.unwinding` の死んだ panic のエントリが残ったまま — Go は recovery 後に unwound フレームを**一切**出さない（実測: supersede された元 panic の残りも含めて消える） | [#155](https://github.com/podhmo/minigo/pull/155) — `unwinding` エントリを panic タグ付きにして、遷移で consume 側+frame 自身の panic のエントリを除去（outer の live unwind は保持） |
| 新 panic が mid-drain で supersede すると `Callers` の unwound 順が狂う（`f.func2` が `g,f` の後に埋まる） | `unwinding` が panic をまたいで append 順一本 — Go は**新しい gopanic の unwound フレームを先**に、古い unwind の残りをその後に出す | [#156](https://github.com/podhmo/minigo/pull/156) — タグでグループ化し「最後に pop があった panic」を先に出力 |

実測で確定した Go セマンティクス（panic.go の `gopanic`/`recovery` と挙動プローブ）:
- deferred call が panic した時点で元 panic は**死亡**（superseded）— 新 panic を recover しても元 panic は復活せず、関数は正常終了する（`f-d2 recover: P` → `f returned` → `outer: <nil>`）。
- panic が recover で consume されると、その unwind が pop したフレームは Callers から**全て**消える — supersede 歴の残りも含め、live フレームだけが残る。
- `unwinding` のリスト順 = 「panic 単位のグループ化、新しい panic を先」— 単一 append 順では supersede 時に新 panic の frame が古い unwind の残りに埋もれる。

回帰 pin: `text_value_deferpanicrecover`（propagate+stale inflight）、`text_value_defercallersclean`（consume 後の二重表示）、`text_value_defercallerssuper`（panic グループ順）。

#### リファクタリング提案の評価（第7ラウンド）

| 提案 | 判定 | 対応 |
|------|------|------|
| `frame.deferred` は write-only | 正しい — `sentinel` が marker の役割を担う | 削除（#156 内の cleanup コミット） |
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
