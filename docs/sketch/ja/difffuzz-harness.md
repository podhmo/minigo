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

## 5. 再現手順

```
make difffuzz DIFFFUZZ_ARGS="-domain text -seed 1 -batches 8 -per-bucket 1"
make difffuzz DIFFFUZZ_ARGS="-domain text -seed 3 -batches 16 -per-bucket 1 -mask '^\S+ '"
make difffuzz DIFFFUZZ_ARGS="-domain num -seed 1 -batches 4"
make difffuzz-corpus DIFFFUZZ_CORPUS_ARGS="-j 4 -timeout 15s"
go test -run TestDiffRegressions -v .
```
