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
