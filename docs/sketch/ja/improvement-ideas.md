# minigo 改善アイデア集 —— オーナー視点での棚卸し（2026-11）

- status: `triaged` — 精査と振り分けは済んでいる。新規作業はこの文書から直接起こさず、TODO.md または `docs/ja/open-questions.md` 側を見る。
- triage record: [improvement-ideas-review.md](./improvement-ideas-review.md)

コードのオーナーとして「次に何を良くするか」を4つの軸（コード品質 / 機能 / UX / 性能）で洗い出したメモ。
既に TODO.md や `docs/next/ja/TODO.md`（リアーキテクチャ計画）にある項目は重複させず、参照に留める。
各アイデアには **根拠（実測・観察）**、**提案**、**コスト感** を付けた。末尾に優先度表がある。
精査と TODO / open questions への振り分けは [improvement-ideas-review.md](./improvement-ideas-review.md) にまとめた。

前提とする位置付けは `docs/ja/minigo-positioning.md` のとおり ——
「ビルドのない即時実行」が価値の中心で、実行速度そのものはネイティブに勝てない。
したがって改善の優先度も **起動〜最初のフィードバックまでの体験** と **"静かな誤答" を減らすこと** を上に置く。

---

## 0. 今回の観察・実測

| 観察 | 値 |
|---|---|
| `fmt.Println("x")` だけのスクリプトの wall time | **13ms**（`go run` は 2.5s）—— pitch どおり |
| `for i := 0; i < 5_000_000; i++ { s += i % 7 }` | **2.3s**（約 450ns/iter） |
| 同ループの CPU プロファイル上位 | `VM.loop` 30%、`frame.push/pop` 19%、`typeOfValueDepth` → `builtinTypedef` → `Env.Get`（RWMutex + map 検索）**約 25%** |
| 同ループの alloc | `intBinOp` が 80MB（`runtime.Value = any` による int の interface boxing、`convT64`） |
| `minigo vet .` に `undefined: undefinedThing` を含むファイル | **exit 0**（実行すると trap） |
| panic 時のトレースバック | 絶対パス・行のみ（列なし）・Python 風 `File "...", line N, in f()` |
| `go test .`（root 権限のコンテナ） | `os_err_script_path` が失敗（後述 C2、変更とは無関係の既存問題） |

---

## 1. 性能（Performance）

### P1. ローカル変数ストアの "同種 bare scalar" 早期 return を前に出す【即効・小】

**根拠**: `vm.retypeStore`（`vm/vm.go`）は `x = x + 1` 型のホットパスに `sameBareScalar` の早期 return を持つが、
その **前に** `ct = v.typeOfValue(oldElem)` を無条件に計算している。bare な int の `typeOfValue` は
`builtinTypedef("int")` → `H.Builtin` → `runtime.(*Env).Get` で、**RWMutex の RLock/RUnlock + 文字列キーの map 検索** を毎ストア支払う。

**試行**（計測のみ、コミットせず）: `constPayload(val)` が非定数かつ `sameBareScalar(oldElem, val)` のとき、
`ct` を計算する前に `return val` するよう 3 行移動しただけで、上記ループが **2.34s → 1.86s（約 -20%）**。
意味論上、この分岐では `ct` は使われない（Named 比較も const 再型付けも通らない）ので、等価変換のはず。
差分テスト（difffuzz の回帰コーパス、strict lazy conformance）で確認してから入れる。

### P2. 組み込み型 typedef の取得を lock-free にする【小】

**根拠**: P1 を入れても `typeOfValue` 自体は代入以外（比較・変換・`%T` など）で頻繁に呼ばれ、毎回 `Env.Get` を通る。
universe スコープ（`int`, `string`, `error`, …）は Engine 構築後に不変。

**提案**:
- `VM` に `builtinTD [kindCount]*runtime.TypeDef` のような配列を持ち、`builtinTypedef` を初回だけ `H.Builtin` で引いて以後は配列参照にする。
- もしくは universe 用 `Env` を「構築後 freeze された mutex なし map」に分ける。他の `Env` も、**書き込みがセッション初期化と REPL 以外にない** なら copy-on-write に寄せられる。
- 期待値: P1 適用後の残り `Env.Get` 由来 CPU（プロファイル上 RWMutex だけで ~10%）がほぼ消える。

### P3. `MaxStack` を連続オペランドスタックの事前確保に使う【中・要再計測】

**根拠**: `frame.push`/`frame.pop` で 19%。ただし精査時点の実装は、frame ごとの独立スライスではなく、
goroutine ごとの連続 arena `v.vals` 上の窓 `vals[base:base+sp]` を既に使っている（P4-3 済み）。
`sp` インデックスも既存なので、残っているコストは arena の append/re-slice・容量更新・stale view 再切断まわりと考えて再計測が必要。

**提案**: `bytecode.Verify` は既に `MaxStack` を計算して返すが、現状は検証時に返り値を使っていない。
これを `Chunk` に保持し、`prepFrame` / 呼び出し側で frame 窓の最大深さぶんを事前に確保・予約できるかを試す。
「shared contiguous value stack を新規導入する」話ではなく、**導入済みの連続スタックの拡張コストを削る**話として扱う。

### P4. 整数ループ向けのスーパー命令（superinstruction）【中】

**根拠**: 典型ループ `for i := 0; i < n; i++` は `load i / const / lt / jmpif / ... / load i / const 1 / add / store i`
と多数の dispatch + push/pop + `any` boxing（80MB）を発生させる。

**提案**: コンパイラ側で頻出パターンを融合する。
- `OpIncLocal slot`（`i++` / `i += k`）
- `OpLtLocalConstJump slot, k, target`（比較 + 条件分岐）
- `OpBinLocalLocal op, a, b`（両オペランドがローカル）

bare `int` の小さな値は Go ランタイムの `staticuint64s`（0..255）で boxing が無料になるが、それ以上は毎回 alloc される。
スーパー命令内部では `int64` のまま計算し、**ストア時に一度だけ** box すれば alloc は半減以下になる。
`Value` 表現そのもの（`any` → タグ付き struct）を変えるのは影響範囲が大きすぎるので、まずはこの局所最適で十分。

### P5. 性能回帰のゲートを CI に置く【小〜中】

**根拠**: TODO.md の perf 系項目は個別に before/after を測っているが、恒常的なガードがない
（「oapi-codegen が #662/#675 で壊れた」件と同じ構図が速度でも起こりうる）。

**提案**:
- `engine_bench_test.go` 等から 5〜8 本の代表ベンチ（整数ループ、map 更新、メソッド呼び出し、host 呼び出し、`text/template` Execute、起動時間）を固定セットとして選ぶ。
- CI で main と PR の両方を `-count=5` で回し `benchstat` 比較、**+15% 以上の悪化で警告**（fail ではなくコメント）。
- 起動時間（`fmt.Println` だけのスクリプトの wall）は pitch そのものなので、これだけは閾値超えで fail にしてよい。

### P6. `[]byte` のコンパクト表現 —— open question への推奨【大】

`docs/ja/open-questions.md` の readfile-mem 問題（32MB の `os.ReadFile` で ~560MB RSS）について、オーナーとしての推奨は
**案A（ホスト `[]byte` を backing にしたコンパクトスライス）を段階導入** する方。

- 段階1: `runtime.Bytes` 型を追加し、**読み取り専用経路**（`len`, index read, `range`, `string(b)`, host への受け渡し）だけ対応。書き込みが来たら従来の `[]Value` に materialize（copy-on-write）。
- 段階2: `append`/`copy`/`b[i] = x` をネイティブ化。
- `.Elems` 直接参照（精査時点で test を除く `.go` ファイル約 313 箇所）は、まず `SliceLike` インターフェイス経由のアクセサに機械的に置換するリファクタを先行させる（ロジック変更ゼロの PR として分離できる）。

案B（GoValue boxing）は例外ケースが増え続けるので取らない。

---

## 2. コード品質（Code improvements）

### C1. `budgets.go` の exact-match をやめ、上限 + 片方向ラチェットにする【小】

TODO.md / #1396 の既存項目への具体案。
- `TestFileBudgetsMatchRepo` を「measured ≤ budget」に緩める（増加だけ検出）。
- 予算を下げる作業は定期ジョブ（または `make budgets-tighten`）で一括実施。
- これで「祖先が merge されるたびに全 PR が conflict」が解消される。ファイル肥大の抑止効果は保たれる。

### C2. 環境依存テストのガード【小】

- `TestDiffRegressions/os_err_script_path` と `TestStrictLazyConformance/os_err_script_path` は、**root で実行すると** `walkdir a/locked open a/locked: permission denied` 行が go 側にも出ず期待値とずれて失敗する（パーミッションが root には効かない）。
  case 側で `os.Geteuid() == 0` を検出して該当行をスキップするか、ハーネスに `// requires: non-root` 的なメタデータを足す。
  クラウド/コンテナで開発する機会が増えているので、clean checkout で `make test` が緑になることを優先したい。
- 同種の既存項目: REPL completion テストのローカル失敗（TODO.md #1393）。

### C3. trap / エラーに安定した識別子を付ける【中】

**根拠**: 現在 trap はメッセージ文字列のみ（`runtime trap: undefined: x`, `binary 0 on int64 and bool` …）。
メッセージを改善するとテスト期待値と差分コーパスが一斉に揺れる。

**提案**: `Trap` に `Code`（例: `MG-UNDEF`, `MG-UNSUPPORTED`, `MG-HOSTCONV`）を持たせ、
- テストはコードで比較（メッセージ改善の自由度が上がる）
- CLI は `minigo explain MG-HOSTCONV` で「なぜ起きるか・回避策（`--src` / `Bind`）」を表示
- difffuzz の verdict 分類（trap 契約）とも対応が取りやすくなる。

### C4. 巨大ファイルの分割はリアーキ計画（P1: ファイル分割）に乗せる

`vm/composite.go`（3445行）、`vm/assert.go`（2359行）、`vm/binop.go`（1777行）など。
`docs/next/ja/TODO.md` の P1（機械的分割・ロジック変更ゼロ）で扱う。ここでは追加提案しない。
ただし **P3/P4 の性能改修と同時にやらない**（diff が読めなくなる）ことだけ順序として明記しておく。

---

## 3. 機能の追加・洗練（Features）

### F1. `minigo check`（または `vet --deep`）—— 「到達したら落ちる」を事前に知る【中】

**根拠**: 「コンパイルは決して失敗しない、未対応は到達時に trap」という設計は正しいが、利用者は
**自分のスクリプトが最後まで走るか** を実行するまで知れない。`minigo vet` は未登録 stub の検出に加えて
`os.ReadFile` の performance hint も既に出すが、関数本体を eager compile して `OpTrap` を列挙する検査はなく、
`undefined: undefinedThing` を含むファイルでも exit 0 を返す。

**提案**: 対象パッケージ（**import 先は含めない**）の全関数を strict lazy compile（`WithStrictLazyCompile` の経路）で eager にコンパイルし、
`OpTrap` が埋め込まれた位置を `file:line:col: message` で列挙する。
- import 先は index だけ（lazy のまま）なので、`go/packages` 的な eager ロードにはならない —— 制約に抵触しない。
- trap の種類で分類: 未定義名 / 未対応構文 / 型不一致（gate）/ 未バインド host メンバ。
- `--json` 出力で editor / CI 連携。
- gopls が既にエラーを出すケースとの重複は許容（gopls が無い CI でも検出できる価値がある）。

### F2. `minigo test` —— ビルドなしでテストを回す【中〜大】

**根拠**: 「入力が変わるたびに即実行」は生成スクリプトだけでなくテストにも当てはまる。
`examples/test-detect` はテスト対象の検出であって実行基盤ではないが、`tools/tmpltests` は stdlib の `*_test.go` を
minigo 上で走らせる generated driver と最小 `testing` shim を既に持っており、こちらが直接の実証になる。

**提案（最小形）**:
- `_test.go` を含めてパッケージを読み、`TestXxx(t *testing.T)` を列挙して順に実行。
- `testing.T` は host 側の最小実装を `Bind`（`Errorf/Fatalf/Run/Helper/Cleanup/TempDir/Skip`）。
- 出力は `go test -v` 互換（`--- FAIL: TestX (0.00s)`）にして既存ツール（gotestsum 等）に流せるようにする。
- benchmark / fuzz / `TestMain` は非対応で明示。
- 用途: minigo スクリプト自身の単体テスト、`examples/*` のスクリプト群。

### F3. `minigo run --watch`【小〜中】

起動 13ms という強みを最も体感できる機能。ファイル変更（`fsnotify` 相当だが依存を増やさないならポーリングで十分）で再実行する。
将来は `PackageImage`/`PackageInstance`（リアーキ M5）を使い、変更されたパッケージだけ破棄して再 index する incremental 版に発展させる。

### F4. スクリプトとしての直接実行（shebang 互換）【小】

Go ファイルに `#!` は書けないが、`gorun` 系で知られる
```go
///usr/bin/env minigo "$0" -- "$@"; exit
package main
```
のイディオム（1行目が `//` コメントとして Go 的に合法で、シェルからは実行される）を公式にサポート・ドキュメント化する。
`--` がない形だと script 引数が minigo 側の positional argument として解釈され、複数引数で `unexpected arguments` になることを確認した。
`--` 入りなら `os.Args` は `["./script.go" "a" "b"]` のように素直に届くので、実装はほぼ不要で、README とテストを足すだけ。
「Go で書くシェルスクリプト」の用途が明確になる。

### F5. `Engine` API の型付きヘルパ【小】

埋め込み側が `runtime.Value` から Go 値へ変換するボイラープレートを減らす。
```go
v, err := minigo.CallAs[string](ctx, e, pkg, "Render", data)
```
内部は既存の snapshot / host 変換を使い、変換不能時は型名入りのエラーを返す。

---

## 4. UX の洗練

### U1. トレースバックの読みやすさ【小】

現状:
```
minigo: panic: runtime error: invalid memory address or nil pointer dereference
Traceback (most recent call first):
File "/tmp/.../t1/main.go", line 7, in f()
		func f(m map[string]*P) int { return m["a"].X }
```
提案:
- パスを **cwd 相対** に（`./main.go:7:38`）。エディタ/ターミナルでクリック可能な `path:line:col` 形式に統一。
- `Instruction.Pos` は列も持っているので、ソース行の下に **キャレット `^`** を出して式の位置を示す（`m["a"].X` のどの部分か）。
- 関数名をパッケージ修飾（`main.f`）。
- interpret された stdlib 内部フレームはデフォルトで折りたたみ（`... 4 frames in text/template ...`）、`--full-trace` / `MINIGO_TRACEBACK=all` で全表示（Go の `GOTRACEBACK` に倣う）。
- TTY のときだけ色付け。

### U2. "did you mean" ヒント【小】

`undefined: Prnitln` のような trap に、同スコープ（パッケージ index・import 済みパッケージのメンバ）から編集距離の近い候補を添える。
index は既にあるので追加コストは trap 時のみ。`docs/sketch/ja/similarity-algorithms.md` の知見も流用できる。

### U3. 未対応・未バインドのときに "次の一手" を示す【小】

host 変換や未バインドメンバの trap では、利用者が取れる行動は限られている（`--src pkg` / `Bind` を足す / `gen-intrinsics`）。
メッセージに 1 行のヒントを付ける:
```
minigo: runtime trap: encoding/hex.AppendEncode is not bound
  hint: run with --src encoding/hex, or bind it (minigo gen-intrinsics -output ... encoding/hex)
```
C3 のエラーコードと組み合わせると `minigo explain` に誘導できる。

### U4. 終了コードを Go と揃える【小】

コンパイル済み Go バイナリは未回収 panic で **exit 2** になる。minigo は現在 exit 1。
ラッパースクリプトや CI で「panic」と「スクリプトが `os.Exit(1)` した」を区別できるよう、panic / trap は 2 にする
（trap を別コード、例えば 3 にする案もある —— 「minigo の未対応」と「スクリプトのバグ」を CI で見分けられる）。

### U5. REPL の小さな改善【小】

- `:type <expr>` —— 値を評価せずに（もしくは評価して）型を表示。
- `:time <stmt>` —— 1 行の実行時間。
- 括弧が閉じていない入力の継続行（複数行貼り付け対応）は `repl.IncompleteInput` と `.. ` prompt で既に実装済み。
- 補完は TODO.md の experiment 項目の結論次第。

### U6. `minigo help` に用例を【小】

`minigo help` は usage のみ。`minigo help examples` で「ファイル1枚の実行」「エントリ関数指定と引数」「`--src` の使いどころ」「プロファイル」の 4 例程度を出す。
環境変数（`MINIGO_CPUPROFILE` など）の一覧も `minigo help env` にまとめる。

---

## 5. 優先度表

効果（体験 / 正しさ / 速度）とコストから、着手順の提案。

| 順 | ID | 内容 | 効果 | コスト |
|---|---|---|---|---|
| 1 | P1 | `retypeStore` の早期 return 前倒し | 整数ループ -20%（実測） | 極小 |
| 2 | C2 | root 環境でのテスト失敗ガード | clean checkout で緑 | 小 |
| 3 | P2 | builtin typedef の lock-free キャッシュ | 型照会全般が軽く | 小 |
| 4 | U1 | トレースバック（相対パス・列・キャレット） | デバッグ体験 | 小 |
| 5 | U4 | 終了コードを Go 互換に | CI / ラッパー互換 | 小 |
| 6 | C1 | budgets を上限 + ラチェットに | PR conflict の解消 | 小 |
| 7 | F1 | `minigo check` | 「最後まで走るか」を事前に | 中 |
| 8 | P5 | ベンチの CI ゲート | 速度回帰の検知 | 小〜中 |
| 9 | U2/U3 | did-you-mean / 次の一手ヒント | 初見の詰まりを減らす | 小 |
| 10 | P3 | 連続スタックへの MaxStack 事前確保 | arena 拡張/re-slice 削減（要再計測） | 中 |
| 11 | F4 | shebang 互換の公式化 | スクリプト用途の明確化 | 小 |
| 12 | C3 | trap コード | メッセージ改善の自由度 | 中 |
| 13 | P4 | 整数スーパー命令 | ループの alloc 削減 | 中 |
| 14 | F3 | `--watch` | 即時フィードバック | 小〜中 |
| 15 | F2 | `minigo test` | ビルドなしテスト | 中〜大 |
| 16 | P6 | コンパクト `[]byte` | 大ファイル処理の RSS | 大 |

P1〜P2・C2・U1・U4 は独立した小 PR で、それぞれ before/after を添えて今すぐ着手できる。
F1 と F2 は「位置付け」ドキュメントの「不足している部分（code walk / checker）」に直結する機能なので、中期の柱として推したい。
