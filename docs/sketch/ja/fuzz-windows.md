# Windows 環境での動作確認 — `make test` とファズハーネス全系列の再実行

対象: `podhmo/minigo` の `main`（言語仕様回 `fuzz-language.md`・ユースケース回 `fuzz-difffuzz-report.md`・並行回 `fuzz-concurrency.md`・convert-define 回 `fuzz-convert-define.md` がマージ済みの状態）
方法: これまでの動作確認は全て Linux (ubuntu) だったので、Windows (Server 2022 + Git Bash/MSYS + Go 1.27.1) で `make test` が通るか、そして 4 系列のファズハーネスが Linux と同じ判定になるかを確認する。ハーネスはそれぞれのメモに書かれた設計を Windows 用に写して再現したもの（`~/usecasefuzz` は podhmo/minigo-usecasefuzz を clone、`~/langfuzz`・`~/concfuzz`・`~/convfuzz` は本メモ末の構成で再構築）。

**最終状態: `make test` グリーン。usecasefuzz PASS=28/ACCEPT=1/TRAP=8、langfuzz PASS=19/PASS-REJECT=2/TRAP=2/DIFF=0、concfuzz 18/18 全 PASS、convfuzz OK=5/WARN-BUILD=1/GEN-FAIL=2 — 4 系列とも Linux 実行時と同一の判定に揃った。**

その過程で Windows 固有の問題を 7 件、インタプリタ本体の移植性に関わらない実バグを 3 件修正した。

## 1. Windows 固有で直したもの

### Makefile / ビルド周り

- **W1. `make format` が壊れていた**: `$(shell find . -name '*.go')` の `find` が Windows `System32\find.exe`（文字列検索コマンド）を拾い、`goimports` が `FIND: ...` を入力ファイルとしてエラーにした。`go tool goimports -w .`（goimports 自身が再帰走査する）に置き換え。同じ `find` 衝突で `go-mod-tidy-all` も壊れていたので `git ls-files` ベースにした。
- **W2. make.exe の不在**: Windows には GNU make が入っていないため mingw64 の `mingw32-make.exe` を `make.exe` としてコピーして使った（環境構築側の話でリポジトリには変更なし）。
- **W3. CRLF 問題**: `git config --global core.autocrlf true` のデフォルト環境で checkout すると全 `.go` が CRLF になり、`"a\n"` を含む golden テスト等が壊れた。`.gitattributes` に `* text=auto eol=lf` を追加して LF 固定にした。

### パス区切り（`\` vs `/`）

- **W4. トレースバックや inspect のパス表示が `\` 混じりだった**: `syntax.ParseFile` に `filepath.ToSlash(filename)` を入れてファイル名を常に `/` 区切りで保持するようにした。`inspect.go` の `Dir` バインド、`minigo.go` の `<file>` 引数の表示名も同様に正規化。`filepath.WalkDir` 等のホスト API が返すパスは OS ネイティブのまま（`go run` 側も同じ `\` を出すので diff oracle としては一致する）。
- **W5. `ExecDirField` の等値判定**: `minigo run .` で getcwd のパスが 8.3 短縮名（`ADMINI~1`）やドライブ文字大小違いで来ることがあり、文字列比較だと外れていた。`os.SameFile` で実体比較するようにした（テスト側 `minigo_test.go` も合わせて修正）。
- **W6. `pkg/locator` のテストが POSIX 絶対パス前提**: `/tmp`/`/home` の GOPATH を想定していて Windows では絶対パス判定に落ちなかった。`filepath.Abs` で環境に合わせた絶対パスを作るように修正。

### その他

- **W7. `exec.Command("echo")` を使うテストが動かなかった**: `echo` は Windows では独立 exe ではない（cmd の内部コマンド）。`testdata/fsops/main.go` で `runtime.GOOS == "windows"` のとき `cmd /c echo|cd` に振り分けるようにした。
- **ハーネス側の注意**（リポジトリ外）: `#!/usr/bin/env bash` はこの VM では WSL bash を拾うので `/usr/bin/bash run.sh` で実行する。`timeout`/`sort` も `System32\` の別物を拾うため `/usr/bin/timeout` `/usr/bin/sort` を明示。`go.mod` の `replace` や `-file` 引数に渡す MSYS パス（`/c/...`）は `cygpath -w` で `C:\...` に直してからネイティブバイナリへ渡す必要がある。

## 2. ファズハーネスの再現と判定

### usecasefuzz（PR #30 のハーネス相当）

podhmo/minigo-usecasefuzz をそのまま clone して `run.sh` に Windows 分岐（`minigo.exe` + `/usr/bin/timeout`）と `.gitattributes` を追加。`go run` を oracle にした差分は Linux 実行と完全一致: PASS=28、`inspectuse` は ACCEPT（minigo 独自機能で `go run` がコンパイルできない）、`lim-*` 8 件は全て `sync.Pool`/`sync.Map` 未バインド等の既知の意図 TRAP。

### langfuzz（PR #29 のハーネス相当）

`cases/<name>/main.go` に `func main()` を書き、`(cd dir && go run .)` と `minigo ./dir` の出力を diff。goroutine dump 等が混ざる panic 出力は最初の `panic: MSG` 行までで正規化する。22 ケースで PASS=19 / PASS-REJECT=2（`go` はコンパイルエラー、minigo は `*Trap` で適切に拒否）/ TRAP=2（`lim-complex`・`lim-threeidx` の意図プローブ）/ **DIFF=0**。

### concfuzz（PR #28 のハーネス相当）

`cases/<dir>/main.go` に exported 関数群 + `funcs.txt` 期待値を置き、`timeout 8 minigo ./dir Func` で 18 関数を実行。`once`（sync.Once.Do）・`chanptr`・`chancontainers`・`duration`・`seldefault`・`deferexit`・`loopvar`（Go 1.22 ループ変数）・`sortcall`・`chanrange`・`locks` は全て期待値一致。`deadlock`・`timer` は設計通りの TRAP（select なし受信→デッドロック近似、timer 未実装系）。**18/18 PASS**（TRAP を期待したケースが TRAP したものを PASS に含む）。

### convfuzz（PR #23 のハーネス相当）

`cases/<name>/` に `define.go`（`//go:build codegen`）+ `source/` + `destination/` + 空 `main.go` を置き、`go run . -file define.go -output generated.go` で生成→`go mod tidy && go build` まで走らせる（go.mod の `replace` は `cygpath -w` パスで自動生成）。5 ケースが `OK`（生成物がコンパイル可能）、`leaf-mismatch` は `WARN-BUILD`（期待通り型不一致のコードを出してビルドで弾かれる）、`typo`（存在しないフィールドパス）と `ptrptr`（`dst.Inner.Inner` の `**Sub` — convert-define の既知制限）は `GEN-FAIL`。全て設計通りの判定。

## 3. 見つかったインタプリタ実バグ（Windows 非依存・プラットフォーム共通）

ファズハーネスを Windows で流した結果 2 件の移植性に関わらない残存バグが表面化した（Linux でも再現する）。

- **F1. `uint8(5)` など「変換式」で型タグが落ちる**: `var u uint8 = 5` は宣言時にセル側へ typedef が乗るので `-u` が 251 になるが、`u := uint8(5)` は変換結果が素の `int64` のまま残り `-u` が -5 になった（`u-10` で wrap せず -5、`%T` が `int`）。`convert()` の int 族で、対象がサイズ付き整数（`int8`/`rune`/`byte`/`uint*`/`uintptr`）のとき `Named{Typ: td}` で包んで返すよう修正（`binaryOp`/`unaryOp` 側の re-mask 機構に乗る）。全 int を包むと非型付け値に化けて約 10 件の既存テストを壊したため、サイズ付き名だけに絞っている。
- **F2. 定数式の fold が無く `1<<100>>50` が int64 演算で 0 になる**: `compile.go` の `binary` に `foldConst`/`constValue` を追加し、両辺が定数の `BinaryExpr`/`UnaryExpr`/`ParenExpr`/`Ident` 列を `go/constant` で評価して `OpConst` を直接 emit するようにした。bool/string/float64/int64（`Int64Val` 失敗時は `Uint64Val`→`GoValue{uint64}` box）を生成し、表現不能な結果（`1<<100` の単独値）は `constant overflows int64` の compile trap、`x / 0`（分母が定数 0）は `constant division by zero` の compile trap にする — Go のコンパイルエラーと対応。go/constant のパニック（負シフト等）は `constValue` 内の `recover` で「定数でない」扱いに落としてランタイム経路に戻す。
- **F3. `%T` が `rune`/`byte` のエイリアス名をそのまま出していた**: Go は `var r rune` の `%T` を `int32` と出す（エイリアスは独立した名前を持たない）。`intrinsics.go` の `typedefSpelling`/`anonTypeSpelling` で `byte`→`uint8`、`rune`→`int32` に canonicalize。`int64(5)` の `%T` が `int` になる既存の括り（宣言名でなく幅名が出る系）は踏襲して残す。

## 4. 残課題

- `const B = 1<<100` のように「宣言だけ・まだ使われていない大きすぎる定数」は Go では宣言自体は合法だが、minigo では init 実行時に eager に trap する（use 側で制約する Go より厳しい。設計制限 `lim-bigconst` の範囲）。
- `[]byte`/`[]rune` の要素取り出しは素の `int64` で帰る（セルに typedef が残らない）ため `b[0] - 'a'` のような式は Go では型エラー、minigo では静かに計算される。
- `Makefile` は Git Bash 環境前提（PowerShell 直打ちやネイティブ make では動かない）。
- ファズハーネス（langfuzz/concfuzz/convfuzz）は VM ローカルのため CI 化するなら usecasefuzz 同様リポジトリ化が必要。

## 5. 変更まとめ

- `syntax/syntax.go`: `ParseFile` で `filepath.ToSlash`。
- `inspect.go` / `minigo.go` / `minigo_test.go`: Dir バインドと `<file>` 表示名の slash 正規化、`os.SameFile` 比較。
- `pkg/locator/locator_test.go`: GOPATH を `filepath.Abs` で作る。
- `testdata/fsops/main.go`: Windows では `cmd /c echo|cd`。
- `Makefile`: `goimports -w .` と `git ls-files` ベースの `go-mod-tidy-all`。
- `.gitattributes`: `* text=auto eol=lf`（usecasefuzz リポジトリにも）。
- `vm/vm.go`: `convert()` でサイズ付き整数変換を `Named` ラップ。
- `compile/compile.go`: `foldConst`/`constValue` による定数畳み込み（trap: overflows-int64 / division-by-zero）。
- `intrinsics.go`: `%T` の byte/rune 正規化。
- `testdata/fuzzfix/main.go` + `minigo_test.go`: `ConvSizedInt` / `ConstFoldShift` / `ConstDivZero` の回帰ケース。
- `~/usecasefuzz`（podhmo/minigo-usecasefuzz）: `run.sh` の MINGW 分岐、`.gitattributes`、`.gitignore` に `out/minigo.exe`。
EOF
echo done