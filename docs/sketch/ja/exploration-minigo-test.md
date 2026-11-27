# 検討: `minigo test` の実装設計

`docs/sketch/ja/improvement-ideas.md` F2 と `docs/ja/open-questions.md` `test-command`（判断待ち）に対する設計メモ。
「公開コマンドにするか・最低互換範囲をどこまでにするか」を決めるための材料として、既存の部品で何が組めるか・何が欠けているかを整理する。

前提の実証: `tools/tmpltests` は stdlib の `*_test.go` を verbatim で minigo 上に走らせる仕組みを既に持つ（scratch GOROOT + `testing`/`flag`/`iter` のソース shim + 生成ドライバ + 1 テスト 1 プロセスの verdict 採取）。`minigo test` はこれを「エンジン内で完結する supported command」に置き換える話であり、subprocess・ドライバ生成・RESULT 行プロトコルの大部分が不要になる。

## 1. `go test` の構造を minigo へ写す

`go test` が内部的に組み立てるものは 4 つある。

| go test の構成物 | minigo での対応物 |
|---|---|
| 本体 + 内部 test ファイルの test variant（`foo [foo.test]`） | `meta.GoFiles` = 非テスト + `package foo` 句の `*_test.go` で build した `runtime.Package`。通常 load と同じ import path キーに入れるので、後から外部テスト側の `import "foo"` がそのままこの variant を引く |
| 外部テストパッケージ（`foo_test`、variant を import） | 第 2 の `runtime.Package`（Path=`mypkg/foo_test`、Name=`foo_test`）。内部 test を含んだ `foo` を先に engine キャッシュへ publish してから load するだけで、特別な配線は要らない |
| `testing` パッケージ（`T`/`B`/`F`/`M` と runner） | host 側 facade を `e.Bind("testing", ...)` で runner 専用 engine にだけ bind する。`T` は `stdlib.HostType("testing.T", ...)` で typedef ごと露出できるので、スクリプト側の `func TestXxx(t *testing.T)` のパラメータは同じ TypeDef に解決される。import alias（`import tt "testing"`）も file-scope の import 経由で同一 typedef に行くので許容される |
| 生成 `TestMain` ドライバ | 不要。runner が index から `TestXxx` を列挙して `Engine.Call` を順に呼ぶ。tmpltests の `zzz_driver.go` 相当がまるごと消える |

## 2. 必要な部品

### 2.1 `_test.go` を含むファイル列挙

除外は 2 箇所にある: `resolve.ReadPackageFiles`（build constraint 適用後の GoFiles）と `resolve.FileScopeFiles`。一方で `resolve.ScanRepo`/`RepoPackage` はすでに `GoFiles`/`TestFiles` を分割して持っており、`pkg/modulewalk` も `TestImports` を分けている。

必要なのは `LocateDir`/`Locate` の test 版: `ReadPackageFiles` と同じ MatchFile 規則（`//go:build`・`_GOOS/_GOARCH` 接尾辞・Tags）を `*_test.go` にも適用し、package 句で `foo` / `foo_test` に振り分ける薄い層。resolver インターフェースを変えずに、runner 側で dir を直接列挙する形が素直（`ScanRepo` 的な列挙 + `ctx.MatchFile` 評価）。

### 2.2 `testing` facade（host bind 案 — 決定）

2 案あった。

- **(a) host facade**: `internal/` か `x/` に最小 `testing.T` 実装を持つ Go パッケージを作り、`Bind` で `testing` として登録。runner が T インスタンスを作り、verdict を host 側で直接回収する。
- **(b) source shim**: tmpltests の `testing.go` shim を `//go:embed` して「ソース文字列から package を作る」経路を新設する（`e.newPackage` + `indexFiles` で実現可能だが、現状そういう公開入口はない）。

**(a) で決定（podhmo 判断、2026-11）。** 理由:

- verdict の回収が RESULT 行の parse ではなく Go の値になる。`t.Fatalf`/`t.Skip` の分類も sentinel で正確に扱える
- `testing.Short()`・`TempDir()` など runner のフラグ状態を facade が直接読める
- `t.Run` の subtest は facade が script func を `VMCaller.Call` で呼ぶ既存の callback 経路で動く
- source shim だと `Short()` が runner 側のフラグを読む手段が別途要る（bound helper との混在は面倒）

`testing` の bind は runner 専用 engine に限定する。既定の stdlib テーブルに入れると、普通の `minigo run` で `import "testing"` が shim を引いてしまい、使い道がないのに挙動を変える。

#### `FailNow`/`Skip` の運搬方法（設計上の要点）

本物の `t.Fatal`/`FailNow` は `runtime.Goexit` で unwind する — **recover() では捕捉できない**。panic で実装すると（tmpltests shim は panic）、スクリプト側の `recover()` が failNow を飲み込んで gc と挙動がずれる。

host facade では「builtin `Fn` が sentinel error を返す」経路が使える。VM boundary の error は trap として伝播し recover() 対象にならないので、Goexit の semantics に panic より近い。runner は `Engine.Call` の返り error が sentinel かを見て FailNow/Skip/PANIC/TRAP を仕分ける。tmpltests が panic 駆動だったのは「スクリプト側に runner を書いていた」制約由来で、host facade では error チャネルの方が正しい。

### 2.3 テスト列挙とシグネチャ検査

`p.Index`（または `p.Files` の AST）から top-level の `func TestXxx` を拾う。`Test` 接頭辞 + パラメータが `*testing.T` 1 つのみ。

パラメータ判定は構造的にやる: `*ast.StarExpr` の内側が `SelectorExpr{X: ident, Sel: "T"}` で、`X` がそのファイルの import 表で `testing` path に結び付くか。型解決そのものはしない（import path ベースの同一性で十分。`type T struct` をローカルに持つケースは import 解決で弾ける）。

拾う対象と扱い:

| 形 | 扱い |
|---|---|
| `func TestXxx(t *testing.T)` | 実行対象 |
| `func TestXxx(*testing.T)`（無名パラメータ） | 同上（go test も許容） |
| `func ExampleXxx()` | v2 で対応。tmpltests の `scanExamples`/`exampleOutputEqual`（`// Output:` 比較、unordered 対応）がそのまま移植できる |
| `func BenchmarkXxx(b *testing.B)` | 明示的に非対応と報告（存在を検出したら "not supported" と言う。黙って無視しない） |
| `func FuzzXxx(f *testing.F)` | 同上 |
| `func TestMain(m *testing.M)` | 同上。go test では TestMain が entry point なので、無視して通常実行すると「TestMain が skip したはずのテストまで走った」になりうる — 存在を検出したら loud に非対応を報告する |

### 2.4 実行モデル

`go test` の semantics は「パッケージ init 1 回 → テストを逐次実行」。`Engine.Call` は call ごとに fresh VM を立てるので:

- **デフォルト**: package を一度 init し、テストごとに `Call`。script panic / trap は per-test の verdict として捕捉され、suite は止まらない（panic で suite ごと死ぬ実 `go test` より厳密には良い）
- **trap 後の package state**: trap は interpreter 限界なので中途半端に壊れた globals を引きずる可能性がある。残りテストを新しい session（= package 再 init）で続行するオプション（`--isolate` または auto-isolate-on-trap）を用意するのが現実的。tmpltests の process-per-test に相当するが、in-process なので session 再建で済む
- **cwd**: `go test` は各パッケージの dir を cwd にして実行する（`testdata/` の相対パスのため）。minigo でもパッケージごとに engine/session の cwd をその dir にする（`NewEngine(dir, ...)` で十分）

### 2.5 出力と終了コード

`go test -v` 互換に揃える（gotestsum 等に流せるのが狙い）:

```
=== RUN   TestXxx
    foo_test.go:12: log/error output
--- PASS: TestXxx (0.00s)
=== RUN   TestYyy/sub
    --- FAIL: TestYyy/sub (0.00s)
ok      mypkg/foo   0.123s
FAIL    mypkg/bar   0.045s
```

verdict は tmpltests 系譜の 5 値を踏襲: PASS / FAIL（Error/Fatal 発火 = 出力や期待値の divergence）/ SKIP / PANIC（script panic）/ TRAP（minigo の未実装面）。

終了コードは単純に: **非 PASS（FAIL/PANIC/TRAP）が 1 件でもあれば exit 1**（go test 互換）、それ以外は 0。verdict 行で FAIL と TRAP は区別されるので、CI で分けたい場合は出力を見れば足りる。別コードを割り当てる案は `exit-codes` の open question が決まったら再検討でよい（podhmo 判断: やりやすい方法で、2026-11）。

### 2.6 `t.Helper()` と `file:line` 出力（TODO へ — 決定）

`go test` の `foo_test.go:12:` は `t.Helper` + `runtime.Caller` で動く。host facade 側には現状、script 側の呼び出し位置を知る経路がない。

**v1 は近似で出し、caller-position capability は TODO.md に切り出す**（podhmo 判断、2026-11）。近似: runner がテスト関数の宣言位置を知っているので、失敗行を `func TestXxx` の宣言行で出す。`file:line` の file は合っていて line が荒い — v1 の documented divergence として許容する。

本実装の選択肢（TODO 側の作業）:

- `VMCaller` に caller position を返す capability を足す（`t.Errorf` 相当が呼ばれた script 側の file:line が取れる）。一番正直だが新規の小さな機構
- special form は `pkg.Name` 呼び出しのみ対象で `t.Errorf` のメソッド呼び出しには張れないので、この用途には使えない

### 2.7 `testing.T` の面

最小セット（improvement-ideas F2 の契約）: `Error/Errorf/Fatal/Fatalf/FailNow/Failed/Log/Logf/Helper/Cleanup/TempDir/Skip/Skipf/SkipNow/Run/Name/Deadline`。

判断が要るもの:

- `t.Context()`（Go 1.24+）— test の context を返す。実装は容易なので入れてよい
- `t.Setenv`/`t.Chdir` — プロセス状態を弄る。in-process 実行だと host process への漏洩が危ない。os 系が `bind.Env`（FsPath/Cwd/AllowedRoots）の sandbox を通っているなら、Chdir は「engine の script cwd を変える」実装が安全側。無理なら trap（loud）に倒す
- `t.Parallel()` — 逐次実行モデルなので no-op または明示 trap。serial 実行で正しさを保てる限り no-op の方が無難（記録だけする）
- `t.Attr`/`t.ArtifactDir`/`t.Output`（新しめ）— 後回しでよい
- `testing.Short()` — runner の `-short` flag を facade が返す

`flag` パッケージ: テストファイルは `flag.Parse()` や `-test.run` など testing flag を踏むことがある。tmpltests は no-op shim を入れていた。`minigo test` の `-run` 系フラグを script 側の `flag` に見せるかは要検討 — v1 は tmpltests 同様 no-op shim（runner 専用 bind）でよい。

## 3. `--tags` について（storybook 用途への回答）— 2 つの別物

`minigo run` の `--tags`（→ `resolve.BuildConfig{Tags}` → `WithBuildConfig`）は **build だけを変える**フラグで、実行する対象は別途 `--entry`/位置引数が明示する。だから run 側ではこれで完結する。

`minigo test` は同じ形では収まらない。**実行するテストの集合はファイル列挙から導く**ので、「どのファイルを対象とするか」がそのまま「どのテストを実行するか」になる。ここには 2 つの別物の概念がある:

1. **`--tags`（go test -tags 互換: build を変えるだけ）** — タグを build config に足し、`//go:build story` の非テストファイルや `//go:build story` 付きの `*_test.go` を package に含める。**実行集合は絞らない**: build に入った全テストが走る（untagged のテストも含む）。通常の Go コードで `go test -tags story` したときに「タグがあるものだけ対象」にはならないのと同じ — 既存テスト + タグ関連ファイルが対象になる
2. **タグ裏のファイルにだけあるテストを実行するモード（storybook 用途）** — `//go:build story` でのみ選ばれるファイルに宣言されたテスト**だけ**を実行し、untagged のテストは実行しない。これは go test にない minigo 固有のオプション

storybook 的な利用では 2 の方が欲しいもの: `--tags story` 相当の build（story helper が解決できる）の上で、実行集合を tag-gated ファイル由来のテストに絞る。**1 と 2 は別フラグに分ける** — `--tags` は build の話として go 互換の意味を保ち、2 に別名を与える。`--tagged <tag>`: `<tag>` を build にも加えつつ、実行集合を `inTagged && !inPlain` —— `x/storybook` の `scanDir` と同じ semantic —— で選ばれたファイルのテストに絞る。

細部: `--tagged` でも untagged の `*_test.go` は build には含める（ヘルパー共有・コンパイル検査のため）。実行しないテストは `-v` で「skipped (untagged)」と数える程度でよい。storybook の `StoryXxx` 実行そのものは test ではないので、そちらは `minigo run --entry` 系や `x/storybook` の Render 経路が担うべき範囲。

## 4. スコープ案

### v1（最小の公開形）

- 対象: `minigo test ./dir` のみ（podhmo 判断、2026-11。`<import path>` ref・`file.go` 直指定・`./...` 展開は後）
- internal test ファイル（`package foo` の `*_test.go`）をマージして build、`TestXxx(*testing.T)` を逐次実行。外部 `package foo_test` と `ExampleXxx` は v2（検出したら "not supported" と言うか黙ってスキップかは実装時に決める — 前者が無難）
- `testing.T` の最小セット + `testing.Short()`
- `-run <re>`（`TestX/sub` の `/` 区切り subtest match）、`-v`、`-short`、`-count`、`-timeout`、`-failfast`、`--src`/`--deny`/`--tags`（`run` と同じ plumbing、build のみに効く）、`--tagged <tag>`（tag-gated ファイル由来のテストだけ実行、§3）
- 出力は `go test -v` 互換。非 PASS があれば exit 1（§2.5）
- Benchmark/Fuzz/TestMain は検出したら loud に "not supported"

### v2 以降

- `package foo_test`（外部テストパッケージ）— internal test ヘルパーも variant に含まれるので「`_test.go` に置いた export helper を `foo_test` から呼ぶ」まで動く
- `ExampleXxx` + `// Output:` 比較（tmpltests の実績コードを持ち込める）
- `minigo test ./...` — `resolve.ScanRepo` の `HasTests` フィルタで逐次実行（ネスト module は `go -C` 相当の cwd 切替で）
- `-cover`/`-race`/`-bench*`/`-parallel` の本実装は当面対象外と明示
- `--isolate`（trap 後の session 再建）

## 5. 実装ステップ案

1. **test 用ファイル列挙**: dir の `*.go` を package 句 + MatchFile（tags 込み）で `foo` / `foo_test` に振り分ける薄い層。`resolve` 側に小さく足すか runner 内に持つかは量次第
2. **runner コア**: `internal/testrun`（仮）— variant package の build（`meta.GoFiles` + internal test files で `buildPackage` 相当）、index からの test 列挙、`Engine.Call` ループ、verdict 集計、`go test -v` 風出力
3. **testing facade**: `T`（HostType）+ 最小メソッド群 + `Short()`。sentinel error で FailNow/Skip を運搬。runner 専用 engine に `Bind("testing", ...)`
4. **`cmd/minigo` に `test` subcommand**: `run` の flag 解析と同じ hand-rolled 方式に揃える
5. 以降: external `foo_test`、Example、`./...`、`--tagged`（tag-gated 由来の実行絞り込み）、`t.Context`/`t.Chdir` 判断

置き場所の候補: runner は `internal/testrun`（CLI 専用なら internal で十分）、facade は `internal/testrun/testing` または `internal/stdlib/testing`（既定テーブルには登録せず runner からのみ install）。`x/` は storybook のような「minigo を組み込む側の公開 utility」なので、cmd 直結の runner より向きが違う。

## 6. `test-command` の open question への回答材料

このメモの結論としては「**最小 runner を公開コマンドとして提供する**」方が筋:

- tmpltests は「テストを"検出して"子プロセスで回す」ハーネスで、in-process runner とは役割が違う（stdlib verbatim corpus の網として残る）
- 新規の実行機構はほぼ要らない（variant package 列挙 + `Engine.Call` ループ）。v1 で新設が必要なのは testing facade だけ（caller-position は TODO 送り、§2.6）
- 「experiment command から始める」中間案は、`minigo test` 自体の性質上ほぼ実装コストを変えない。スコープを v1 に絞る方が実質的な段階付けになる

podhmo 判断済み（2026-11）:

- testing facade は host bind 案（§2.2）
- `--tags`（build のみ）と `--tagged`（tag-gated 由来の実行絞り込み）は別フラグ（§3）
- v1 の対象 ref は `minigo test ./dir` のみ。外部 `foo_test`（同一 dir で `package foo_test` を宣言するファイル —— package ref 形式とは別の論点）と Example は v2 送り（§4）
- exit code は単純に「非 PASS → exit 1」。TRAP 用の別コードは `exit-codes` 決定時に再検討（§2.5）
- `t.Helper`/`file:line` の本実装（caller-position capability）は TODO.md に切り出し、v1 は宣言位置の近似（§2.6）
