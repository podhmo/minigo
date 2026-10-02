# 差分ファジング用ハーネスの作り直し — 設計と試行結果のレポート

対象: `podhmo/minigo` の `main`（#30 マージ後）
問い: #23 / #28 / #29 / #30 で行った「思考駆動の探索的テスト」を、ハーネスの作りでもっと上手くできるか？

結論から書くと、**「人が考えて書いたプログラムを `go run` と比べる」作業の外側を、機械が回せるループにする**ことで、同じ程度の時間でより多くの、しかも小さく再現しやすいバグを、再現可能な形で出せた。一方で手書きのユースケース（[podhmo/minigo-usecasefuzz](https://github.com/podhmo/minigo-usecasefuzz)）が得意な「プログラム全体の構造」は、生成では代替できない。両者は補完関係にある。

本セッションでは minigo 本体の修正はしていない（ハーネスとレポートを優先）。見つけたバグは `testdata/difffuzz/` に PENDING 付きの回帰ケースとして置き、TODO.md に積んだ。

## 1. 既存のやり方の観察

#28〜#30 の流れは次の通り:

1. 人（エージェント）が観点を考え、プログラムを数十本手で書く（#29: 約40本、#30: 28本 + lim-* プローブ）
2. `go run` を oracle として出力を diff（`run.sh`）
3. 差分を見つけたらその場で直す
4. 修正ごとに `testdata/fuzzfix` 等に期待値を手書きした回帰テストを足す

良い点: 観点の質が高い。json の tag、`errors.As`、`os.Args` を flag が読むなど、「実際のスクリプトがどこで躓くか」を突いており、1本のプログラムで多数の機能の組み合わせを踏める。

ハーネスとして弱い点:

- **規模が人の手で頭打ち**。1ラウンド数十本。観点の外にあるバグは原理的に出ない。
- **判定がプログラム単位**。1本の中で最初に死んだ箇所より後ろは観測されない。差分が出ても「どの式が悪いか」は人が二分探索する。
- **判定基準の穴**。`run.sh` は minigo 側の出力に `panic:` があると TRAP（許容）扱いにする。minigo の契約は「未実装は loud な trap」なので trap は許容してよいが、*Go では panic しないのに minigo が panic する* のは誤動作であり、許容側に入れてはいけない。
- **縮小・重複排除・優先度付けが手作業**。同じ根本原因のバグが別の形で何度も現れても、それを束ねるのは人の頭。
- **再現性**。コーパスはリポジトリ外（`~/concfuzz` 等は既に存在しない。usecasefuzz は別リポジトリとして残っている）。回帰テストの期待値は手書き。

## 2. 作ったもの

`tools/difffuzz`（Go 製、リポジトリ内、`make difffuzz` / `make difffuzz-corpus`）。詳細は `tools/difffuzz/README.md`。

### 2.1 判定を minigo の契約に合わせる

「Go と一致するか」ではなく「Go と一致する、または loud に trap する」を合格とする。

| verdict | 意味 | バグか |
|---|---|---|
| PASS | Go と同じ出力 | |
| TRAP | 一致した出力の後で `runtime trap:` で停止 | いいえ（実装バックログ） |
| SILENT | trap せずに出力が違う | **はい** |
| CRASH | インタプリタ自身が panic | **はい** |
| HANG | Go は終わるのに timeout | **はい** |

SILENT はさらに症状で分類する: `value` / `type`（`%T` だけ違う）/ `missing-panic` / `spurious-panic` / `panic-message` / `unrecovered-panic` / `panic-became-trap`（Go では recover できる panic が、ホスト関数のコールバック内で起きたために recover 不能な trap になる）。

### 2.2 式レベルの生成 + probe 単位の隔離

1プログラムに約200個の *probe* を入れる。probe は1つの型付き式で、各々が `recover` 付きのクロージャで評価され `id: %T %v`（または `id: panic: …`）を1行出す。

- Go は1プログラムにつき1回だけ実行すればよい（各行が独立）。
- minigo が途中で止まったら（trap/crash/hang）、止まった probe にその判定を付けて、次の probe から再実行する。1つの trap でバッチ全体が無駄にならない。
- panic も観測対象になる（ゼロ除算・範囲外アクセス・負のシフト量など）。

生成器は2ドメイン:

- **text（既定）**: minigo の主用途であるテキスト処理・LL 的スクリプト。`strings`/`strconv`/`fmt` の動詞/`unicode`/`utf8`/`regexp`/`sort`/`slices`/`maps`、`[]string`/`[]byte`/`[]rune`/`map[string]int`、さらに「range over string」「単語カウント」「`strings.Builder`」「switch」「重複除去」などの文の形を IIFE テンプレートとして持つ。**シグネチャ表駆動**で、`sig("Name", 戻り型, "テンプレート $1 $2", 引数型...)` を1行足せば対象面が広がる。
- **num**: sized/named int、float、シフト、変換、複合代入、`++/--`。優先度は低い。

同じ式を別の経路で評価する**メタモルフィックな文脈**も混ぜる: ジェネリック関数 `id[T]` 経由、構造体フィールド経由、クロージャ呼び出し経由、defer 内代入経由。

### 2.3 自動縮小（バッチ化）

差分が出た probe は貪欲に縮小する。1ステップで作れる全候補（同型の部分式への置換、葉変数への置換、変数を「単純な値」に差し替え、文脈の除去、根を任意型の部分式に差し替え）を**サイズ順に1プログラムへ並べる**。各行は独立なので「最初に食い違った行 = 最小の失敗候補」になり、1ラウンドが go build 1回 + minigo 数回で済む。

縮小では verdict だけでなく **症状も保存**する。これをしないと、支配的なバグ（例: `rune` が `%T` で `rune` と出る）にすべての発見が吸い込まれる — 実際に最初の試行ではそうなった。

### 2.4 トリアージ支援

- 縮小前に指紋（verdict・症状・両側の型・文脈・根の演算子）でグルーピングし、グループごとに小さい順で数件だけ縮小（コスト削減）
- 縮小後の形で重複排除（sized int は符号クラスに粗視化）
- **派生の畳み込み**: 縮小後の式が別の発見の縮小式を部分式として含むなら「derived」として親に畳む（擬似的な根本原因クラスタリング）
- **`-mask REGEXP`**: 既知の差分を比較前に隠す（Csmith でいう既知バグ抑制）。`-mask '^\S+ '` で型を無視し値だけ比較できる
- trap はシグネチャごとに集計し、各シグネチャの代表例も縮小する → 「何を実装すれば何件救えるか」の表になる

### 2.5 生成器の自己検証

oracle 側（gc）が生成物を拒否すると、ハーネスのバグが minigo のバグに見える。対策は2段:

- `TestGeneratedProgramsCompile`: 生成した probe と**全縮小候補**が gc でコンパイルできることをテストで保証。実際にこれで縮小器のバグを2つ見つけた（左オペランドが定数になった二項 `-` を単項として描画していた／`lit << v` の型が文脈依存で `t := …` と噛み合わない）。
- 実行時は gc 自体を最終フィルタにする（`-gcflags=-e` のエラー行から probe を特定して SKIP にし、再ビルド）。生成規則が完全でなくても止まらない。

### 2.6 回帰テストへの受け渡し（xfail 方式）

`gen -emit DIR` で、各バグの縮小版を「単体で `go run` できる `main.go` + Go の実際の出力 `want.stdout` + `PENDING`」として書き出す。`testdata/difffuzz/` に置くだけでルートの `TestDiffRegressions` が実行する:

- `PENDING` があるケースは「まだ食い違うこと」を確認して skip
- minigo が直って Go と一致すると「PENDING を消せ」で **fail** する → 修正が自動的に回帰テストとして固定される

期待値を人が書かないので、修正セッションの手間が「直す → PENDING を消す」だけになる。

### 2.7 修正ループのスキル化

`.claude/skills/difffuzz/SKILL.md`: `/difffuzz hunt`（新しい seed で探索 → 1原因1ケースで testdata に積む → TODO.md）、`/difffuzz fix [slug]`（優先度の高い PENDING を選ぶ → 原因修正 → PENDING 削除 → 同じ seed で SILENT が増えていないことを確認 → make format/lint/test → PR）、`/difffuzz extend <area>`（シグネチャ表を足す）。別セッションで「1根本原因 = 1PR」を回す前提。

## 3. 結果

### 3.1 text ドメイン（主対象）

| 実行 | probes | 時間 | PASS | TRAP | SILENT |
|---|---|---|---|---|---|
| seed=1（型も比較） | 1600 | 1m35s | 843 | 209 | 548 |
| seed=3（`-mask '^\S+ '` 値のみ） | 3200 | 1m20s | 2680 | 375 | 145 |

seed=3 の SILENT 145件は、縮小・派生畳み込み後に12バケットにまとまった。根本原因としては次の通り（括弧内は `testdata/difffuzz/` のケース）:

1. **nil スライス・nil map の表示が `<nil>`**（`[]` / `map[]` が正しい）。`fmt.Println(ss)` で出るので、スクリプトで最も踏みやすい。`%#v` も `[]string(nil)` にならない。（`text_value_05d448e7`, `text_value_c971b474`, `text_value_17998073`）
2. **`%q` に空の `[]string` を渡すと `""`**（`[]` が正しい）。（`text_value_3613fc16`）
3. **多値呼び出しをそのまま `fmt.Sprint` に渡したときの空白規則**: `fmt.Sprint(strings.Cut("a=b", "="))` が Go では `abtrue`、minigo は `a b true`。Sprint は「両側とも文字列でないときだけ空白を入れる」。（`text_value_650e3d07`, `text_value_6d405433`）
4. **ホスト関数のコールバック内の panic が recover 不能な trap になる**: `strings.Map`/`strings.TrimFunc` に渡した関数の中で範囲外アクセスすると、Go では `recover` できるが minigo は `runtime trap: panic: …` で停止する。（`text_panic-became-trap_*`）
5. **スライス範囲外 panic のメッセージにインデックスが無い**（`slice bounds out of range [2:0]`）。（`text_panic-message_66b1011f`）
6. **`slices.Clone(nil)` が nil でない**。（`text_value_f50116fd`）
7. **`%T` の差**（見た目のみ、優先度低）: `rune`/`byte` が `int32`/`uint8` と出ない、`strings.Fields`/`regexp.FindAllString` の戻りが `interface{}`、`s[i]` や `unicode.ToUpper(r)` が `int`。（`text_type_*`）

trap 側（実装バックログ。縮小済みの代表例付き）:

| 件数 | シグネチャ | 縮小後の例 |
|---|---|---|
| 93 | `unsupported types: runtime.Nil + int64` | `c := maps.Clone(m); c[k]++` — `maps.Clone` の結果で欠けたキーがゼロ値にならない。単語カウントで踏む |
| 67 | `undefined: strings.IndexByte` | 未バインド |
| 45 | `undefined: strings.FieldsFunc` | 未バインド |
| 28 | `cannot convert *runtime.TypedNil to string` | `string(nilRunes)` / `string(nilBytes)` |
| 23 | `maps.Clone: arg must be a map` | `maps.Clone(nilMap)`（Go は nil を返す） |
| 1 | `cannot use <nil> as int` | map のコピー + delete の組み合わせ |

このうち上の2件と `string(nil []byte)`、`maps.Clone(nil)` は LL 用途でそのまま踏むので、trap であっても優先度は高い。

### 3.2 num ドメイン（参考）

| 実行 | probes | 時間 | PASS | TRAP | SILENT |
|---|---|---|---|---|---|
| seed=1 | 800 | 6m22s | 460 | 142 | 198 |

根本原因はおよそ4つ:

1. **型付き変数宣言が型を失う**: `var f float64 = 3` が `int` になり、**`f/2 == 1`**（Go は 1.5）。`var total float64 = 0` のような普通のスクリプトでも値が狂うので、num ドメインだがこれは優先度が高い。`var x int64 = 0` も `%T` が `int`。
2. **sized int への変換結果が型を失う**: `int8(x)` が `int`。
3. **unsigned の扱い**: `uint` の 2^63 以上が負数で表示、`uint64` の `/`・`>>` が符号付きで計算される、`-(uint)` が wrap しない。
4. **unsigned のシフト量**: `x << uint(1<<63)` を負のシフト量として panic（Go は 0）。#30 の lim-sha と同根。

num は縮小対象が多く遅い（数分）。症状の多くは同じ原因の型違いなので、最終キーを符号クラスに粗視化して重複を減らした（seed=3・値のみ比較・3200 probe で、縮小後のバケット数 105 → 67）。それでも 67 バケットが上の4原因程度に収束するので、num ドメインの重複排除は text より明らかに弱い。

### 3.3 corpus: `$GOROOT/test` の `// run` テスト 145本

Go 本体のテストは自己検査型（結果が違うと panic する）なので、oracle 付きコーパスとしてそのまま使える。minigo は Go の完全なサブセットを目指していないので、TRAP の大半（`unsafe`、GC/finalizer、`runtime` 内部）は対象外。

PASS 47 / TRAP 65 / SILENT 23 / CRASH 1 / HANG 3 / SKIP 6。LL 用途でも踏みうるものだけ拾うと:

- `reorder.go`: 多重代入の評価順（`[1 100 3], want 100,2,3`）
- `typeswitch1.go`, `switch.go`, `range.go`: 自己検査の失敗
- `const8.go`: `undefined: iota`（特定の書き方で）
- `initialize.go`: **インタプリタ自身の CRASH**（`ast.BasicLit` を `KeyValueExpr` に型アサーション）
- `copy.go`, `divmod.go`, `makeslice.go`: HANG（15秒超。ループの重い性能問題）
- `goprint.go`: `println` の nil ポインタ表示が `0x0` でなく `<nil>`
- バックログの上位: `sync.Map`（9本）、`time.Duration` を型として使う（2本）

## 4. 比較と評価

| 観点 | 既存（#28〜#30） | difffuzz |
|---|---|---|
| 観点の出し方 | 人が考える | 表 + 乱数（観点は表の行として蓄積） |
| 規模 | 数十本/ラウンド | 数千 probe/分（text） |
| 判定単位 | プログラム | probe（式） |
| 判定基準 | `panic:` も TRAP 扱い | trap と誤 panic を区別、症状分類 |
| 縮小 | 手作業 | 自動（症状を保存、バッチ化） |
| 重複排除 | 頭の中 | 指紋 → 縮小形 → 派生畳み込み |
| 再現性 | リポジトリ外、手順依存 | seed で決定的、リポジトリ内 |
| 回帰テスト | 期待値を手書き | Go の出力を保存、xfail で自動固定 |
| 得意 | 機能の組み合わせ、パッケージ構成、init、I/O、並行性、実在 API の使い方 | 式・標準ライブラリ面の網羅、境界値、表示形式 |

数字で言うと、ハーネス構築込みの1セッションで、修正なしに **text 7件 + num 4件 + corpus 数件の SILENT/CRASH 根本原因**と、件数付きの trap バックログが出た。#30 の B10（fmt の map 出力順）や B13〜15（uint64）は、このハーネスなら表に1行足せば周辺ごと掃ける類のバグ。

### 4.1 限界・注意点

- **式レベルなので、プログラム全体の構造は見られない**。パッケージ間 init 順、goroutine、ファイル I/O、`encoding/json` で設定を読む、のような「ユースケース」は手書きのほうが圧倒的に強い。
- **テンプレートの偏り**。テンプレートに無い API は試されない。表の拡充は人の判断。
- **重複排除はヒューリスティック**。部分式包含による畳み込みは、別原因を同じ親に入れることも、同じ原因を分けたままにすることもある。最後の判断は人。
- **未規定の挙動は oracle にならない**。map 反復順や float→int 範囲外変換などは生成しないように規則で避けている。テンプレートを足すときは注意（スキルに明記）。
- num ドメインの縮小は遅い（数分）。`-per-bucket 1` と `-mask` で緩和。
- emit したケースは probe 用の変数宣言を丸ごと持つので `main.go` が少し長い（読むべきは末尾の `try(0, …)` の1行）。

### 4.2 提案: 2つのループの組み合わせ

1. **usecasefuzz（手書き）で「シナリオ」を探す** → バグが出たら、
2. **その API/形を difffuzz の表に1行足して「周辺」を掃く** → 同種のバグを根こそぎ出し、
3. **emit → `testdata/difffuzz/` に PENDING で積む** → 別セッションで `/difffuzz fix` を 1原因1PR で回す。

usecasefuzz 側も別スキルにする価値はあると思う（今回は作っていない）。骨子案:

- `/usecasefuzz add <テーマ>`: 実際に人が書く形のプログラムを1本書き、`run.sh` で判定。判定は difffuzz と同じ契約（`panic:` を TRAP にしない）に揃える。
- `/usecasefuzz triage`: DIFF を見つけたら、該当 API を difffuzz の `textSigs` に足して周辺を掃く手順へつなぐ。
- 修正は `/difffuzz fix` に合流させる（回帰テストの形式を `testdata/difffuzz` に一本化）。

## 補題: Go 本体のテストスイートで全面チェックする方針（途中で主軸から外した）

### 当初の着想

最初に考えた方針は「`$GOROOT/test` を丸ごと oracle 付きコーパスにする」だった。Go 本体のテストは大半が自己検査型で（結果が違うと `panic("fail")` する、または `.out` と比較される）、`go build` してバイナリを走らせればそのまま期待出力が手に入る。手書きコーパスの数十本に比べて桁違いの量で、しかも Go チームが仕様の端を狙って書いたものなので、「人が思いつかない観点」を外部から持ち込める、という狙いだった。

手元の go1.27.1 での規模（先頭行のディレクティブで分類）:

| 場所 | `// run` | 備考 |
|---|---|---|
| `test/*.go` | 151（うち引数なし・単一ファイル・`package main` は 145） | ほかに `// errorcheck` 147、`// runoutput` 14、`// compile` 12、`// rundir` 等 |
| `test/fixedbugs/` | 646 | 過去のコンパイラ/ランタイムのバグ回帰 |
| `test/typeparam/` | 141 | ジェネリクス |
| `test/ken/`, `chan/`, `interface/` | 40 / 17 / 11 | |

### 行動ログ（このセッションで実際にやった順）

整理後の説明ではなく、実際の操作と観察の順に残す。

1. **前回までの資産の確認**。PR #23/#28/#29/#30 の本文と `docs/sketch/ja/fuzz-usecase.md` を読んだ。#28/#30 の本文に出てくる `~/concfuzz` / `~/usecasefuzz` を `ls` したが、どちらも手元に無かった（usecasefuzz は別リポジトリに移っていた）。→「コーパスがリポジトリ外にあって消える」を弱点の1つとしてメモ。
2. **Go 本体のテストを oracle 付きコーパスに使えないか調べた**。`go version`（go1.27.1）、`$(go env GOROOT)/test` に `.go` が 356 本、先頭行がちょうど `// run` のものが 145 本（`grep -l '^// run$'`）。自己検査型なので期待値を書かずに使える、と判断し、これを主軸にするつもりで進め始めた。
3. **minigo の CLI を確認してビルド**。`cmd/minigo/main.go` を読み、`minigo run <dir>` がカレントディレクトリをルートにパッケージを解決することを確認。`go build -o /tmp/minigo ./cmd/minigo`。
4. **最初の数本を手で試す — 1回目は失敗**。`/tmp/gt` に `go.mod` を作り `235.go`/`64bit.go`/`alg.go` を `<name>/main.go` にコピーして流したが、zsh で `rm -rf *` が「no matches found」になってコマンド列が途中で止まり、GOROOT のパスが空のまま `cp` が失敗。minigo は `no buildable Go source files` を返した（ハーネス側の操作ミス）。
5. **2回目**。GOROOT をクォートした変数に入れ直して再実行。`235.go` は exit 0（出力なし）、`alg.go` は exit 0、`64bit.go` は exit 1 で `runtime trap: undefined: sync.Pool`（`bufio.NewWriter` → `io` の package init）。→「テストの中身に入る前に出力手段（bufio 等）で止まるものがある」が最初の観察。
6. **失敗の出方を確認するための小さな2本**。`var m map[string]int; m["x"] = 1`（nil map 代入）と `var c complex128`。minigo は前者を `error="panic: assignment to entry in nil map"`、後者を `error="runtime trap: undefined: complex128"` として slog で出し、どちらも exit 1。Go は `go run` だと panic でも exit 1 になるので区別できない。→ oracle 側は `go build` してバイナリを直接実行し、本来の exit code（panic は 2）を取ることにした。
7. **ブランチを切って判定ロジック（`run.go`）を書き始めた**。この時点では「Go と同じか」を基準に、stdout の行比較 → exit code → panic メッセージの部分一致、という corpus 用の `Judge` を作っていた。並行して式生成器も作る計画を立てていた（GOROOT コーパス + 生成器の二本立て、主はコーパス）。
8. **ここで「minigo は Go の完全なサブセットを目指していない」という前提を受け取った**。README の「unimplemented constructs emit a trap」と TODO.md（lim-* を設計上の制限として扱っている）を読み直し、判定基準を「Go と一致する、または loud に trap する」に変更。trap を許容側、trap しない食い違い・インタプリタ自身の panic・timeout をバグ側にした（`Judge` に `Trap`/`Silent`/`Crash`/`Hang` を入れたのはこのため）。同時に、GOROOT のテストは GC・unsafe・runtime 内部を大量に試すので、全面チェックを主軸にすると「対象外の機能の trap 一覧」が大半を占めると予想し、**主軸を式レベルの生成器に切り替え、GOROOT コーパスは `corpus -goroot-tests` という補助モードに格下げ**した。
9. **生成器と probe 方式を実装**（本文 §2）。最初の生成器の実行で、SILENT の発見が `%T` の差に吸い込まれる問題などを潰していた。
10. **その途中で補助モードを1回だけ流した**。生成器の再実行をバックグラウンドで回している間に、`go run ./ corpus -goroot-tests -j 4 -timeout 15s` で 145 本を実行。結果が §3.3（PASS 47 / TRAP 65 / SILENT 23 / CRASH 1 / HANG 3 / SKIP 6）。
11. **結果を見て格下げを確定**。TRAP の上位が `sync.Map`・`unsafe.Pointer`・`runtime.SetFinalizer` など対象外、SILENT の多くが `panic: fail` で場所がわからない、HANG は重いループ。corpus の実行中に「主用途はテキスト処理・LL 的スクリプト」という補足も受け取っており、以後は text ドメインの生成器に時間を使った。corpus はこの1回以降は流しておらず、サブディレクトリ（`fixedbugs/` など）は一度も試していない。

### 主軸から外した理由（ログ 8・11 の判断を後から整理したもの）

実際の判断のきっかけは、ログ 8 で受け取った前提（完全なサブセットを目指していない）と、ログ 10〜11 の間に受け取った補足（主用途はテキスト処理）。1回流した結果を後から見直しても、この前提だと Go 本体のテストは主軸に向かない:

- **TRAP のほとんどが対象外の機能**。上位は `sync.Map`（9本、reflect 経由）、`unsafe.Pointer`（7）、`runtime.SetFinalizer`（5）、`runtime.MemStats`/`runtime.Compiler`/`runtime.Breakpoint`、複素数リテラル。GC・ランタイム内部・unsafe を試すテストが多く、「件数順に実装すべきもの」の表としては minigo の優先度と噛み合わない。
- **SILENT が局所化できない**。自己検査型テストは失敗すると `panic: fail` / `panic: 1` のような情報の無いメッセージで止まる。1ファイル数百行のどこで値がずれたかはわからず、結局人が二分探索することになる。これは既存のやり方の弱点（プログラム単位の判定）をそのまま持ち込むことになる。
- **出力手段の段階で落ちる**。`os.Exit` は minigo では設計上 trap（ホストプロセスを終了できない）、`bufio` は `sync.Pool` で止まる、`println` は stderr。テストの中身に到達する前に止まるものがある。
- **性能で HANG する**。`copy.go`/`divmod.go`/`makeslice.go` は網羅ループが重く、15秒で終わらない。バグかどうかの判断がつかない。
- **TRAP→SILENT の誤分類**。`mapclear.go` は自己検査の失敗メッセージを出した後に `os.Exit` で trap しており、「出力が一致した後の trap」ではないので SILENT になる。判定自体は正しいが、原因が「`os.Exit` 未対応」なのか「map の clear が壊れている」なのかは読まないとわからない（実際には後者の失敗メッセージ `number of keys found = 3 want 1` が出ている）。

そこで方針を「Go のテストで全面チェック」から「minigo の主用途に合わせた式レベルの生成 + 縮小」（本文 §2）に切り替え、corpus は**補助モード**として残した。用途は2つに絞った: (1) CRASH/HANG の検出（インタプリタ自身のバグは用途に関係なくバグ）、(2) SILENT の中から LL 用途でも踏みうるもの（多重代入の評価順 `reorder.go`、`switch.go`、`range.go` 等）を拾う。

### やらなかったこと・やるなら

- `fixedbugs/`（646本）、`typeparam/`（141本）、`ken/` 等のサブディレクトリは流していない。`typeparam/` はジェネリクスの単一化（monomorphize）の検証として価値がありそうなので、次に流すならここから。`-goroot-tests` をサブディレクトリにも対応させるのは小さな変更。
- `// runoutput`（生成したプログラムを実行する）、`// rundir`（複数ファイル/パッケージ）、引数付きの `// run` は対象外にした。
- `// errorcheck`（147本）は「Go がコンパイルエラーにするものを minigo が実行してしまう」逆方向の検査に使えるが、README の「compiler never fails」の設計と衝突するので見送った。
- 対象外の領域（unsafe・runtime・GC・複素数）を事前に除外するフィルタ（import や識別子で弾く）を入れれば、残りを LL 用途の回帰として毎回流す運用はあり得る。
- 自己検査型テストの SILENT を局所化するには、テスト内の `panic` 呼び出しを `println` に置換して最初の失敗だけでなく全失敗を出させる、などの書き換えが必要になる。手間に見合うかは未検証。

## 5. 再現手順

```
make difffuzz DIFFFUZZ_ARGS="-domain text -seed 1 -batches 8 -per-bucket 1"
make difffuzz DIFFFUZZ_ARGS="-domain text -seed 3 -batches 16 -per-bucket 1 -mask '^\S+ '"
make difffuzz DIFFFUZZ_ARGS="-domain num -seed 1 -batches 4"
make difffuzz-corpus DIFFFUZZ_CORPUS_ARGS="-j 4 -timeout 15s"
go test -run TestDiffRegressions -v .
```
