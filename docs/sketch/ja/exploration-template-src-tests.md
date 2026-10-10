# 検討: `--src` のテンプレート系と Go 本体の text/template テスト

TODO.md の次の2項目をどう進めるかの検討。

- **source text/template: field access on a parenthesized pipeline panics**
- **html/template from source cannot run**

## 解釈の確認: 対象は「ソース解釈」側

この2件は「minigo からネイティブバインディング経由でテンプレート操作をする」話ではなく、「minigo が GOROOT のテンプレート系パッケージのソースを解釈して実行できるようにする (`--src`)」話。

- bound `text/template` (ネイティブ側) の課題は別エントリ「oapi-codegen needs `--src` for seven packages」に書かれている通り `FuncMap` が無くスクリプト関数を呼べない点で、そちらは解消しない方針で `--src` 運用が進んでいる。
- したがってこの TODO は「minigo 自身が template 系コードを実行可能にする」側を指す。

## usecase の検討

`--src` でテンプレートが動くと何が嬉しいか。

1. **スクリプト製 `FuncMap` を持つコード生成器**。`template.FuncMap` にスクリプト側の関数を渡す用途は bound パッケージでは原理的に不可能で、`--src` 一択。oapi-codegen が実例 (テンプレート関数を大量に登録する)。mockery/moq/wire/stringer 系の codegen ツールもほぼ同じ形をしているはず。
2. **html/template を使う生成器**。HTML を吐く系 (ドキュメント生成・静的サイト生成・メールテンプレート)。エスケーパが text/template の parse tree を作り変えるので、ソース実行であることが特に効く。
3. **部分ファサードの外側の API**。bound `text/template` は New/Parse/Must/Execute + HTMLEscape 系だけ。`Funcs`/`Option`/`Clone`/`AddParseTree`/`Templates`/`Lookup`/`Root.String()`/`block`/range の break/continue など、bound が持たない面に依存するスクリプトはソース解釈でしか動かない。
4. **遅延読み込みの用途の出力層**。minigo の売りは「必要な部分だけ読む」軽さだが、結果をテンプレートで整形するスクリプト (inspect の結果をレポート化する等) でもテンプレート実行が要る。
5. **忠実度の検証基盤** (メタな usecase)。実用コードの正しさを測る網として Go 本体のテストスイートが最大の oracle になる (後述)。

## 現状の障害 (実測・原因特定済み)

### a) `(index .Items 1).Name` panic

`{{(index .Items 1).Name}}` を `--src text/template` で実行すると `reflect: call of reflect.Value.Field on struct Value` で panic (native は `b` を表示)。括弧は本質ではなく、`{{with index .Items 1}}{{.Name}}{{end}}` でも死ぬ — **パイプライン/ビルトイン結果へのフィールドアクセス全般**が壊れている。

原因: 解釈実行された `index` builtin の結果型は `reflect.Value` で、スクリプト側の表現は `*runtime.GoValue{V: *RValue}` という箱になる。この箱を外側の `RValue` が `val` に抱えると `Kind()` は `Struct` (td 由来) を返すのに `get()` は `GoValue` を返し、`structOf` が nil → `Field` が trap する。「reflect.Value の facade 箱」を読むとき内側の RValue に畳む正規化が `e.wrap` (または `outVal`) に無いのが筋。

### b) `cannot use int64 as int` (`--src strings`/`--src bytes`/`bytealg` 系)

`strings.Index` の `return stringslite.Index(s, substr)` で trap。bound 側が `int64(...)` キャストで返すと `scriptVal` が `Named{int64}` タグを付け、ソース側の `int` 結果への代入が coerce できない。実際の Go シグネチャは `int` なので bound は `int(...)` を返すべき (`scriptVal(int)` → bare int64)。`intrinsics.go` の `return int64(` は約17箇所 (bytealg/stringslite/syscall.Getpid 等)。実シグネチャが `int` のものを棚卸して直すと `--src strings`/`--src bytes` が通る見込み。注意: タグ変更で `%T` 等の出力が `int64`→`int` に変わる箇所が無いか既存テストで確認要。

### c) 未バインドのメンバ

- `bytes.IndexAny` が無い (html/template の import 連鎖の第一の止まり点)。bytes 他メンバ (ContainsAny 等) も棚卸し。
- `strconv.IntSize` (TODO.md 別項目。json 系経由でも効く)。

## 案: Go 本体の text/template テストを動かす

テーブル抽出 (execTests 等の表だけ抜く) ではなく **テストファイルを verbatim で実行する** 案を推す。理由:

- テーブル外の内部 API (`tmpl.Tree`, `Root.String()`, `AddParseTree`, `Option`, ParseFiles/Glob) も踏める。
- 「テストが動く」をそのまま網にできる。見つかった divergence は difffuzz ピンに落とす既存フローに乗る。

### 構成

1. **scratch GOROOT**: `$GOROOT/src` を symlink farm で複製し、`testing`/`flag`/`iter` だけ shim ソースで上書きする (`build.Default.GOROOT` は `GOROOT` 環境変数を見るので、ビルド済み `minigo` バイナリへの `GOROOT=/tmp/...` 指定で差し替え可能 — 実測済み)。`testing` は bound ではなく shim ソースの方が自然 (stdlib に無い拡張 API を晒さずに済む)。最小セットは `T` + `Error/Errorf/Fatal/Fatalf/Helper/Log/Skip/Run`。
2. **コピー + driver 生成**: GOROOT の `text/template` を作業ディレクトリにコピーし、`*_test.go` を loader が拾う名前に変えて同梱 (`package template` の internal test なので本体ソースと同居する)。`func Test*` をスキャンして `zz_run.go` (`func Run()`) を生成し、`minigo run <dir> Run` で全件実行、per-test の PASS/FAIL/TRAP を出す。parse パッケージ (`package parse` の lex_test/parse_test) も同じ仕掛けで別ディレクトリに。
3. **除外**: `iter` + range-over-func を使う execTest 12件と `unsafe.Pointer` 系は初期は除外 (型 shim でコンパイルは通して実行失敗を許容)。`link_test.go` (`internal/testenv` + `os/exec` で Go ツールチェイン自体を呼ぶ) はファイルごと捨てる。`example*_test.go` は `package template_test` の外部テストなので初期対象外 (`log` shim も要る)。
4. **置き場所候補**: `tools/tmpltests/` (difffuzz と同じ層) か minigo-usecasefuzz の realworld タスク。in-repo の tool の方が CI/回帰に載せやすい。

### 進め方

1. harness を作り、FAIL 一覧を出す (これが divergence 帳票になる)。
2. FAIL を仕分けし、1 root cause = 1 PR で潰す。上の a/b/c は最初の分に入る見込み。
3. exec_test をグリーン化 → `parse` → `html/template` の順に拡大。
4. `iter`/range-over-func、`unsafe.Pointer` 系は別 TODO に切り分け。

### 残る疑問

- harness を `tools/` に置くか、usecasefuzz 側のタスクにするか。
- FAIL が想定外に大量に出た場合の triage 運用 (difffuzz 同様 1 root cause = 1 PR を想定)。

## バインディング判断の変遷と TODO

このラウンドで「bound (ネイティブバインド) ↔ ソース解釈」の境界をどう動かしたかの記録。bind するとそのパッケージは host 実行になる (速いが script 側の型を見えなくする) ので、判断は性能ではなく正確性が先。

### 足した bind

- **`fmt` Scan 系 (`Sscan`/`Sscanf`/`Sscanln`/`Scan`/`Scanf`/`Scanln`/`Fscan`/`Fscanf`/`Fscanln`)** (#747)。`--src text/template` が `parse/node.go` の `fmt.Sscan` で全滅していたため。script ref → host var の mirror + `SetRef` write-back で実装し、第2パスレビューで「write-back が宣言タグを消す」「out-param 0個で arity trap」の2バグを追加修正。
- **`bytealg.MaxBruteForce`** (#749)。`--src strings`/`--src bytes` が `bytealg.IndexByte` 経由で参照する定数。`int` を返すのが正 (int64 タグ事故の主戦場だった箇所)。
- **`bytealg.MaxLen`** (同上)。arch 依存定数のため `bytealgMaxLen()` で GOARCH 追随に変更。amd64 は CPUID 無しの保守値 31 (IndexString 適用可否の閾値なので小さい側は常に安全)。レビュー子の指摘で発覚。
- **`os.DirFS`** (#756)。`TestParseFS` が `os.DirFS(dir) fs.FS` を要求。ただし後述の通り `io/fs` 本体は bind しないまま。

### 足さなかった (または撤回した) bind

- **`bytes.IndexAny`**: 計画時は html/template の第一の止まり点として bind 候補だったが、int-tag 修正で bound `bytealg.IndexByte` 経由でソース側 `bytes.IndexAny` がそのまま動くことが分かり bind せず。
- **`io/fs`**: `os.DirFS` と同じ流れで bind を検討したが、bound `io/fs` は `embed.FS` (script struct) の script メソッドを原理的に呼べないと判明 → 撤回。`io/fs` はソース解釈維持で、衝突したのは「host 生成の同綴り container が宣言型を失う」問題として marshal/retag 側で解決 (`stampContainerTyp` 系)。
- **`errors.AsType`**: `TestExecError_CustomError` が必要とするが、generic host func の instantiation bind 機構がない。TODO `[ ]` エントリとして残した (未実装)。
- **`unsafe.Pointer` 型**: `--src sort`/`internal/reflectlite` の壁。型+変換の実装が要るので TODO `[ ]` として残した。

### この経緯で追加した TODO

- **`--src` template hot-path 計測**: 「bind を止めた分遅くなるのでは」という疑問への回答として、支配コスト (ノード毎の minireflect 往復 + embed.FS 解釈走査) を計測してから fast-path を決める、という調査タスク。盲 fast-path 禁止の注記つき。
- **nil retag の綴り一致による型混同**: 第2パスレビュー指摘。same-spelled twin retag は設計上の機構だが、非修飾名一致は別パッケージ同名型と衝突しうる。repro なし・直すなら shape 証明が要るので TODO に留めた。
- その他、harness 初回スキャン由来の TODO (FieldByName 昇格、`%T` fmtValue、TestMaxExecDepth、Uint on uint8、`&` on package-level interface var) は各回で随時 `[ ]` 登録済み。

## round-1: harness 構築と FAIL 潰し

### 実施内容（おまけ）

- `tools/tmpltests` (scratch GOROOT + `testing`/`flag`/`iter` shim + driver 生成) を #745 で導入し、upstream `text/template` テスト 46 件を verbatim 実行可能にした。
- FAIL を 1 root cause = 1 PR で潰し、34 → **42 PASS** (stack #766: #747, #749, #750〜#755 系, #753, #756, #757, #759, #770)。TODO 起点の2件 (evalField panic・html/template 系) は FAIL 集合の一部として処理された。
- 残 FAIL/TRAP 4件は全部再現手順つきで TODO.md に記録済み (後述)。

### 計画外の記録と判断

計画時の理解と実施後の理解がずれた点と、その時点で下した判断の記録。

- **evalField panic の本質は「括弧」ではなかった。** 計画では `{{(index .Items 1).Name}}` の括弧つきパイプラインを疑っていたが、実態は `reflect.Value` を返す builtin 結果全般が `*runtime.GoValue{V: *RValue}` の二重箱で minireflect 正規化をすり抜け、`evalField` に壊れた値として届くことだった。→ 判断: `wrap` で内側 view を採用する修正 (#750)。テンプレート側ではなく minireflect 側のバグ。
- **`bytes.IndexAny` の bind は不要になった。** 計画では html/template の第一の止まり点として bind 候補に挙げたが、`int` API が `int64` タグで返る問題 (#749) を直すと bound `bytealg.IndexByte` 経由でソース側 `bytes.IndexAny` がそのまま動いた。→ 判断: bind せず、実測で足りることを確認した上で bind 案を捨てた。
- **`os.DirFS`/`io/fs` の bind 案は途中で撤回した。** bind すると `io/fs` 経由で embed.FS (script struct) の script メソッドを呼べなくなることが判明 — bound io/fs は原理的にこの形を満たせない。→ 判断: `io/fs` はソース解釈維持を前提に、問題を marshal 側の same-spelled container retag として解き直した (#756)。「bind すれば済む」という初期想定が崩れた典型例。
- **`illegal number syntax: "0x"` の正体は lexing ではなかった。** 最初はリテラル字句解析起因と推測したが、実態は bound call に渡った `uint64(1<<63)` が `int64` デフォルトで materialize されて overflow panic → fmt.Format の `%!x(PANIC=...)` 出力が後続 lex を壊す連鎖だった。→ 判断: converted const は代入境界と同じく target 型で materialize (#753)。
- **AssignableTo/Implements の欠損は1層ではなく3層だった。** 計画では「host-td ↔ script-iface の経路が無い」1件と読んでいたが、実態は `Type()` が host box の実 `reflect.Type` を返さない (#757) + `typeOfValue` が `IfaceNil` を処理しない (#759) + typedef-backed host 型の `Implements` が host メソッドセットを見ない (#770) の積層。→ 判断: 各層を別 PR に分けて潰した (1 root cause = 1 PR の維持)。
- **自分の修正が新しい shape を露出させた (レビュー子セッションの検証で発覚)。** `typeOfValue` が IfaceNil のタグを返すようになった結果、成功経路が生の `IfaceNil` を返し始め、`TypeAssert` の `p == nil`・`Elem()` のアクセサ (Len 等) が gc と不一致に。→ 判断: 境界で `TypedNil` に正規化する規則 (vm の `unboxAsserted` と同型) を `Elem()`/TypeAssert に適用し、pin も両パターンを網羅するよう拡張。バグ系の指摘は全て採用、リファクタ系は「dead code の normVal 重複」「kindOfValue の IfaceNil arm」は採用、「中間コミット squash」は不採用 (force-push リスク > bisect hygiene、merge 時 squash で代替可能)。
- **`%T` が `*minigo.fmtValueN` を印字する行だけ残った。** standalone (同一テンプレート+同一データ+`New().Funcs().Parse()`+struct field 経由) では一切再現しない — 実テストファイル環境にのみ発生する別 divergence。→ 判断: 深追いを切り上げて再現条件つきで TODO.md に記録し、次の FAIL に進む。
- **レビュー依頼の「main..tip 全差分」は字義通りには tip PR のみを意味した。** stack のブランチは sibling (各 PR が main 起点) で、main..tip = #759 の差分だけだった。→ 判断: 字義解釈のレビューを採用し、union 全体のレビューが必要なら別途実行することを報告に添えた。

- **レビュー第2パス (union 差分) の要否判断。** stacked PRs の全体差分 (`main..ledger`) を再レビューさせた結果の採否: 採用 = DBG println 残骸の削除、scanFn の write-back が宣言タグを消す件 (named スキャンターゲット)、同一綴り slice retag が N/CapN を落とす件 (`stampContainerTyp` 化)、`-pkg` トップレベル・`-only` 事前フィルタ・shim Cleanup/Deadline・CommandContext 化 (いずれも harness 側)。不採用 = nil retag の「綴り一致」型混同 (repro なし・機構は設計上の twin — TODO 記録に留めた)、host method-set 列挙の共通ヘルパ抽出・scanReader の receiver 除去 (対称性の薄い cosmetic、churn > gain)。
- **初回レビューの範囲誤認をユーザー指摘で修正。** 当初 `main..tip` を字義解釈して最終 PR のみをレビュー対象にしたが、stacked PRs の文脈では全体差分を意味した。sibling-cut 構成のため `git diff main..<tip branch>` では union merge の差分を取る必要があった — 第2パスは ledger ブランチで再実行した。
- **「stacked PRs」は base 指定ではなくブランチ内容の連鎖を意味した (第2の範囲誤認、ユーザー指摘で修正)。** stack 管理は各 PR の `base:` を連鎖させるだけで、ブランチの中身は全て main 起点の sibling-cut のままだったため `main..tip` が全体差分にならなかった。→ 判断: 各ブランチの自コミットのみを下位ブランチの先端へ cherry-pick で積み直し (計20コミット) して force-push。再構築後の先端ツリーはレビュー済み union とコード差分ゼロ (+doc 章のみ) を検証済み。
- **「repro なし」で不採用にした綴り一致 retag は、フレッシュな第3レビューで repro 済み実バグに変わった。** 第2パスでは「形状証明が要るはず (tdShapeEval)」と読み、採用しなかった。第3パスの指摘は別の切り口 — 値側タグが host-minted (`Spec == nil`) かで弁別する方法で、形状証明は不要だった。→ 判断: `sameSpelledTwin` 述語に4箇所を集約して採用 (リファクタ指摘と同一箇所のため同時に解消)、Named arm の kind ガード欠落も修正。前回判断の覆しを明示しておく。
- **`fmt.Fscanf` は bind 後も実は panic していた (head arity)。** scanReader が `heads=2` に対し 1要素しか返さず `head[1]` で落ちる — bind した家族の中で Fscanf だけが2 head を要する非対称があった。→ 判断: 変換器が返さなかった head を scanFn 側で goNative 補完する契約に変更 + 全9 binding に `Target` 設定。
- **harness 自体に false-PASS バグが3件あった (テストを採点する側のバグ)。** `FailNow` が failed を立てない・driver が panic/Skip 時に RunCleanups を飛ばす・`fail()` の祖先伝播が1段のみ。→ 判断: shim を go test のセマンティクス (FailNow = Fail + Goexit 相当・cleanup は unwind でも実行・失敗は全祖先へ) に寄せた。harness を先に作った価値はここにもあった — 審判自身をレビュー対象に含めてよかった。
- **GoValue{*RType} 経由で `*minireflect.RType` が漏れていた。** `reflect.ValueOf(t).Type()` が gc の `*reflect.rtype` ではなく内部構造体名を返していた。`*RValue` 用の既存ガードと同型のリークで、`*RType` 側は valueOfValue が facade struct ごと host reflect.Value に映していたのが根因。→ 判断: host descriptor を view する形に揃え、Interface() も型を返せるようになった (副次修正)。

### 残りの状況

`TestExecute` "range int8" (%T fmtValue 化), `TestIssue48215` (関数ローカル型の埋め込み ptr 昇格), `errors.AsType` (generic bind 機構), `TestMaxExecDepth` (frame limit ポリシー)。次の拡大は `text/template/parse` → `html/template` → `example*_test.go` の順。

## round-2: 非 SKIP 行の全滅と html/template suite

### 実施内容

- 起点 (round-1 残り): `text/template` 42/46 → 本ラウンドで **46/46 PASS**。新規に `text/template/parse` **17/17 PASS**、`html/template` **103/105 PASS** (非 SKIP 行は全滅。残る2件は upstream 側に broken 注記のある正当 SKIP: `TestIssue31810`, `TestTemplateLookUp`)。
- stack #776: #774〜#796 の 22 PR (main 起点は先頭 #774 のみ、以降すべて前ブランチ先端への実コミット連鎖)。make 配線は `make tmpltests` / `tmpltests-parse` / `tmpltests-html` / `tmpltests-all` (#787)。
- 実行は直列の fix worker セッション4本 (text/template 残行 → html 有効化 → marshal 周辺+配線 → html 残行掃討) + 最終検証で見つかった harness バグ1件を coordinator 側で修正。

### 計画外の記録と判断

- **`errors.AsType` は新機構不要だった。** 計画では「generic instantiation の bind 機構が無い」と読んでいたが `runtime.BuiltinFunc.GenFn` が既存で、instantiation 経路も vm にあった。→ 判断: As-walk を GenFn で実装するだけの素直な bind 追加 (#774)。TODO エントリの「機構が不明」という前提が老朽化していた例。
- **`TestMaxExecDepth` は documented divergence にせず harness で正直に通した。** upstream の `maxExecDepth = 100000` は interpreter frame limit (10000) に先に負けるため、到達不能な guard だった。→ 判断: `srcRewrites` で upstream `exec.go` を copy+patch (`maxExecDepth = 250`) し、実コード経路のエラーを frame limit 以下で踏む (#778)。後続で dep パッケージにも rewrite を届ける key 化拡張 (#794 — html suite では text/template が `-src` 側の dep になるため)。「テストを通す」のではなく「テストが検査する本物の guard に届く」形を選んだ。
- **`"range int8"` の `%T` は2段で解いた。** 第1 worker は原因特定まで (`*fmtValue` 箱が accumulator cell の `Named.V` に入り `a[formatAt].(string)` を外して `%T`→`%s` 置換が不発) で deferred — 広い `Cell` deref は `&V{7777}.String()` の描画を壊した。→ 第2 worker は spec スロット復元 (`a[formatAt]` が `*fmtValue`/`Named{string}` のとき `formatString` で flat 側から spec を復元) + `fmtArg` の narrow unwrap で着地 (#782)。値側は非接触で回帰なし。「原因は分かっていたので再挑戦させた」が機能した例。
- **`Funcs("")` / `TestEscapeSet` の犯人はどちらもテンプレートではなかった。** standalone では再現せず suite 内のみ発生した2件は、bisect すると (a) `unfoldTypExpr` が `pkg.T` セレクタを解決せず `FuncMap{"": f}` の map キーをフィールド名定数と誤分類していた (#788)、(b) 関数ローカル型 `[]*dataItem` のフィールドが package index に無く `td=nil` 穴を `Addr()` が無条件 deref していた (#789) — いずれも interpreter 側の型解決バグ。(b) は round-1 残りの `TestIssue48215` (関数ローカル埋め込み ptr 昇格、#777) と同じ「関数ローカル typedef の可視性」の壁の別面だった。
- **`TestParseZipFS` は4層の壁だった。** TODO に「bound archive/zip が Deflate 非対応」と書かれていた表層の下に、別の根本原因が直列に4つ: (a) `opaqueStoreArg` が `*runtime.Named` をポインタ越境し `sync.Map` の decompressors 登録が消失 (#790)、(b) `1<<uint(max)` の untyped 左オペランドが count 側の型を拾う shift typing (#791)、(c) フィールド typedef が `[maxNumLit+maxNumDist]int` の生式を保持し `new()` の fold 済みと綴り不一致 → pointee 同一性 (#792)、(d) `unsafe.Offsetof` が stub で `hash/crc32` init に到達 (#793 — コンパイラがセレクタを (base, "field") に書き換え intrinsic がフィールド順で計測)。→ 判断: 各層を 1 PR ずつ潰し、zip deflate 読み取りが end-to-end で動くところまで潜った。`unsafe.Offsetof` は「`unsafe.Pointer` 型は対象外」の範囲に踏み込まない形 (フィールド名 + オフセット計算のみ) で実装。
- **eval-order `{{.Hello}} {{.N}}` は構造的に不可能と判明して正直 defer。** bound 経路は marshal 時に fields → niladic methods の順で eager 評価するため `hi 1` (gc: `hi 2`)。marshal 順序の入替は diverge を移すだけで、host 呼出しに渡せる遅延 shape が存在しない (`reflect.StructOf` はメソッド合成不可、`map[string]any` は値を eager 化)。`--src` 経路は解釈 `evalField` が script Struct を遅延走査して gc と一致済み。→ 判断: bound 経路の構造的ギャップとして TODO に境界条件つきで記録 (#795)、修正はしない。
- **検証フェーズで harness 自身のバグを踏んだ。** `materializeDep` は dep パスが対象パッケージの parent dirs と重なる場合 (`-pkg text/template/parse` で dep `text/template`) に、parent-dir loop が先に作った symlink 越しに `copyTestFile` → 実 GOROOT の root 所有ファイルへの WriteFile で EACCES (書けていたら実 GOROOT を汚すところだった)。→ 判断: 既存 dst を消してから patch copy (#796)。#794 単体の検証が html suite のみだったため parse suite でのみ発火する経路が残っていた — 「直前 PR の変更範囲以外の suite も回す」を検証手順に含めるべき教訓。
- **bind の追加は「不足分対応」のみ。** 新たに足した bind は `utf8.DecodeLastRune(+InString)`、`bytealg.MakeNoZero/Cutover/CompareString`、`time.Date`+`time.Month`+const 群、`internal/testenv.SetGODEBUG`/`godebug.New`、`bytes.IndexAny/EqualFold/ContainsAny`、`strconv.ErrSyntax/ErrRange/NumError`、`unsafe.Offsetof` — いずれも「ソース解釈が参照するが未バインドで trap する」メンバで、性能目的の差し替えは無し。`bytealg`/`bytes` の追加は upstream ソースを解釈させるより bind した方が正確性が上がる部類 (asm stub 相当)。

### 残りの状況

- **template suite 側**: 非 SKIP 行は全てクリア。残る `[ ]` は `%T` on `&` of interface var (concrete pointee を印字、TODO 新規)、`example*_test.go` (外部 `package template_test` 用の第2パッケージ dir + `// Output:` チェック + `log`/`os` 系未 bind 依存、TODO 記録済み)、range-over-func `iter` 行 (interpreter 側の機能待ち)。
- **周辺の `[ ]`**: bound 経路の eval-order (構造的 defer)、`sync.Pool.Put` の値 alias、host-backed 値のコピー alias — marshal 境界の残件。
- **対象外としたもの**: `unsafe.Pointer` 型 (境界クラス、issue #40)、`--src` template hot-path 計測 (性能動機の作業は今回の指示の対象外)、oapi-codegen `--src` 実走 (下流の実用 epic — 今回の壁除去で到達度は上がっているが「通す」自体は別タスク)。

## Future work: テンプレート登録の遅延（質問への回答メモ・最終）

前提: text/template は verbatim ソース解釈のまま。登録側だけを intrinsic に差し替える最小構成で「Parse を遅延できるか」という意味論の遊びの探索。パフォーマンスではなく「解釈の差異でどこまで進むか」の話。

- **最小構成はこれで成立する。** `(*Template).Parse` を intrinsic 化し「`(t, text, delims)` を pending に記録して nil error を返す」だけにする。実 parse はソース解釈のまま、最初の `Execute`/`Lookup` で遅れて走る。評価器が持つのは eager/lazy の bool 1個だけ (eager = 従来どおり即座に parse して壊れたテンプレートを登録時に検知するモード)。
- **環境捕捉は実害がない。** 遅延 parse が必要とするのは呼出し側フレームではなく `(t, text, delims)` の値と `text/template` パッケージ env で、どちらも呼出側の変数束縛に依存しない。「関数内で Parse して変数束縛の外れた環境で Execute される」ケースでも thunk は値キャプチャで足り、追加の動的環境は要らない。
- **force 点は lookup/execute 側。** pending を記録するだけだと解釈実行される `ExecuteTemplate`/`Lookup` が `t.common` の map を読んでもエントリが無いので、Execute/Lookup も intrinsic にして先に pending を drain する (全 drain でなく、define 名の軽量スキャンで name→text 索引を建てれば per-name force も可能)。define 登録の副作用は遅延 parse の中で起きるので Go の観測順序は壊れない。
- **意味論的に崩れる場所 (許容する差異)。** 壊れたテンプレートは Parse 時に error にならず最初の Execute で (未使用なら永遠に) エラーになる — Go が暗に要求する eager error 契約との不一致で、ここを「実行時に変換」するのがこの設計の本体。`Templates()`/`Clone` は pending を drain するか列挙から漏れるかの分岐。`Funcs`/`Option` を Parse 後 Execute 前に呼ぶと、lazy 側では遅れて追加された func が使えてしまう (Go では Parse 時点の名集合で縛る) — Parse 時に func 名集合をスナップショットするか、許容差異にするか。
- **粒度は有利。** oapi-codegen は 48 ファイルを個別 `Parse` するので force 単位がファイルごと、未使用ファイルは丸ごと skip できる。host lexer 適用後の parse は strict の ~17% — 意味論を曲げて取れる上限はここ。

結論: intrinsic `Parse` (記録+dummy成功) + Execute/Lookup の drain (または per-name force) + 評価器の eager bool、という形で Go の eager error 契約だけを明示的に曲げて遅延できる。thunk が要るのは値キャプチャまでで、動的環境の保持は不要。
