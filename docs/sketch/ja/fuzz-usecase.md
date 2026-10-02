# ユースケース差分ファジング実験 — バグ発見と修正のレポート

対象: `podhmo/minigo` の `main`（言語仕様回 `fuzz-language.md` がマージ済みの状態）
方法: 前回は言語仕様の網羅を狙ったが、今回は「Go の代表的なユースケースがそのまま動くか」を見る。実際に人が書く形のプログラム（テキスト処理・スクリプト用途を優先、バイナリ操作は低優先）を 27 本書き、`go run` を oracle として minigo の出力と diff する。差分が出たら潰す、のループ。加えて `lim-*` プローブで「バインド外パッケージに踏み込んだ時どう見えるか」を観測した。

ハーネス: `~/usecasefuzz/`（`cases/<name>/main.go` + `run.sh`）。各ケースが使っている機能の一覧は `~/usecasefuzz/README.md` を参照。

**最終状態: PASS=28, DIFF=0, TRAP=8（全て lim-* の意図プローブ）, ACCEPT=1（`inspectuse` — `minigo.dev/inspect` は minigo 独自機能で Go ではコンパイルできない意図的なもの）**

初回実行時は PASS=11 / DIFF=8 / TRAP=15 だった。以下の修正を経て全ての通常ケースが Go と一致した。

## 1. 発見したバグと修正

### encoding/json（4 件）

- **B1. `json.Unmarshal(data, &v)` が使えなかった**: 1 引数形式（`dec, err := Unmarshal(data)` のような独自形）しかなく、`&v` に書き込む標準形がなかった。2 引数を新設し、`jsonShape` で typedef 駆動のデコードを実装: struct は宣言フィールドへ `json:` タグ準拠（`-` 除外、大文字小文字のフォールバック含む）で充填、`map`/`slice`/`pointer` は要素 typedef を再帰的に解決、スカラーは宣言型に応じて float64→int64 等へ変換、`runtime.SetRef` で書き戻す。要素 typedef を VMCaller 経由で回収するため `ElemZero(td)` をインタフェースに追加した。
- **B2. `json:` タグが marshal/unmarshal 両方で無視されていた**: typedef の `FTags` が「関数ローカルの `type` 宣言」でのみ設定され、パッケージレベル宣言（`typeDefOf`）や型式の即時 typedef（`compile.go` `typeExpr`、`dispatch.go`、`vm.go` の構造体 typedef 生成）では空だった。`runtime.StructFieldTags` を新設し、生成 4 箇所で設定するよう統一。
- **B3. struct の marshal 順序が違う**: `goJSON` が `map[string]any` を返すため Go のキーソート順（alphabetical）になっていた。encoding/json は struct フィールドを宣言順で出すので、`json.Marshaler` を実装した `orderedObject` で宣言順を保持。
- **B4. `omitempty` 未対応**: `jsonFieldKey` が omit フラグを返すようにし、`jsonIsEmpty`（Go の isEmptyValue 相当）で空値を落とす。

### errors パッケージ（2 件）

- **B5. `errors.As(err, &aerr)` が script 側 error に効かなかった**: `asErr` で `fmt.Errorf` 経由の文字列化をしていたため `*AppError` の型情報が失われていた。`hostErrOf`（script 値を `scriptError` で包む）に切り替え、`scriptError.Unwrap` は script 側の `Unwrap` メソッドを VM 経由で呼ぶ。
- **B6. As の 2 回目呼び出しで誤マッチ**: `&aerr`（`aerr` は `*AppError` 変数）の cell は値として inner Cell を持ち、`cellElemTyp` がそれを見ず want=nil → 全マッチ。inner Cell の typedef を返すよう修正。ついでに `var e error` への `errors.As` はインタフェースターゲットとして全 error を受けるよう補正、`errors.Join` が nil 要素を `<nil>` として連結しないよう `hostErrOf` で untyped nil を nil error に落とす。

### strings / fmt（4 件）

- **B7. `strings.NewReplacer` 未バインド**: `strArgs` で可変長引数を受けてバインド。
- **B8. `strings.TrimFunc`/`IndexFunc`/`Map` 等の関数値引数が使えなかった**: script 関数を `func(rune) bool`/`func(rune) rune` へ橋渡しする `runePred` アダプタを追加し、`TrimFunc`/`TrimLeftFunc`/`TrimRightFunc`/`IndexFunc`/`LastIndexFunc`/`Map` をバインド（エラーは callback 側に保持）。
- **B9. `strings.Builder` が使えなかった**: `hostType("strings.Builder")` を追加し `var b strings.Builder` が `*runtime.GoValue` を生むように。`fmt.Fprintf(&b, ...)` が「`&b` は fmtValue で io.Writer でない」と落ちていたので `asWriter` で fmtValue→Cell→GoValue の unwrap を行う。
- **B10. fmt の map 出力順が不定**: `%v` の map 描画が `Order` の挿入順だった。Go の fmtsort 相当に `lessScript` + 描画文字列のタイブレークでソート。併せて Named の `uint64`/`uintptr` 値は `uint64(iv)` で書き出す（後述の B15 と対）。

### regexp / ホスト境界（1 件）

- **B11. 正規表現の `[][]int` や非マッチの nil スライスが壊れていた**: `FindStringSubmatch` 非マッチで `m == nil` が成立せず `m[1]` でパニック。原因は `goValueOf` で `[]string(nil)`/`[]byte(nil)` が空スライス化し、無名ホストスライス全体が要素毎 unbox されない/されても nil 情報が消えること。`[]string`/`[]byte` は nil のとき `TypedNil`（要素 typedef 付き）を返し、無名ホストスライス/配列は要素毎に `*runtime.Slice` へ unbox（`[]int` 等の FindIndex 系も `m[0]` で読める）に変更。名前付きスライス型はメソッド温存のため従来通り GoValue。

### time.Duration（1 件）

- **B12. `time.ParseDuration`/`d.Hours()`/`d.String()`/`d1-d2` が機能しなかった**: `scriptVal` が `time.Duration` を `int64` に潰していたためメソッドが呼べず、`d/d` の型も狂っていた。Duration を生値として通す方針に転換: `scriptVal`/`goValueOf` は Duration をそのまま返し、`selectMember` に `case time.Duration` を追加して reflect 経由で `.Hours()` 等が呼べる。`binaryOp`/`unaryOp`/`eqlValue`/`numOf` は unwrap→演算→再ラップ（`d/d` は Go 同様 int64）。

### uint64 / リテラル（3 件）

- **B13. `uint64` リテラル `14695981039346656037`（FNV オフセット）が「literal out of range」で死んだ**: `literalValue` で `uint64` 範囲のみ超過したリテラルを `GoValue{uint64}` として保持する（`1<<63` の既存特例は維持）。
- **B14. uint64 の四則演算**: `binaryOp` が `GoValue{uint64}` を int64 に unwrap して演算（mod 2^64 でビット等価）し、結果を再 box。`intOf`/`numOf` に uint 系と GoValue のケースも追加。
- **B15. `uint64(x)` 変換結果が署名付きで `%x`/`%d` が狂う**: `convert` の int 族で `GoValue` をホスト kind から読み、`uint64`/`uintptr` への変換は `Named` タグを付けて符号なしドメインを保持（B10 の fmtValue と対）。

### 型システム境界（2 件）

- **B16. 関数ローカル `type` の typedef が実行時に解決できなかった**: `type Item struct{...}` を `main` 内で宣言して `[]Item{...}` すると OpElemType が package index しか見ず trap。`v.localElemTypedef`（フレーム内の local const セルを走査するフォールバック）を `OpElemType`/`mapZero`/pointer composite/`typeAssert`/`memberOfType` の各サイトに追加。
- **B17. `os.DirEntry`/`os.FileInfo` のコールバック型が未解決**: `filepath.WalkDir` は既にネイティブバインド（相対パス変換 + script funclit への `v.Call` コールバック）だが、シグネチャの `os.DirEntry` が解決不能だった。`MReqs` 空の marker interface typedef としてバインド（`satisfiesIface` は空要件で trivially true、実ディスパッチは GoValue reflect が担う）。

### その他（3 件）

- **B18. `os.Args` が変数ではなく関数バインドだった**: `os.Args()` という独自形だったため、flag パッケージ init の `len(os.Args)` が `len of *runtime.BuiltinFunc` で死んでいた。`AllowedRoots` 無しのときに実プロセス argv の `*runtime.Slice` を変数としてバインド。
- **B19. インデックス付きリテラルの Named インデックス**: `[]T{SomeConst: v}`（stdlib 内部で多発、例 `abi.kindNames`）で `*runtime.Named` インデックスが `int64` 以外で trap。slice/array 両方で unwrap を追加。
- **B20. `fmt.Print`/`Fprint`/`Fprintln` 未バインド**: `h.ffn` ベースで Fprint 族を追加。

## 2. 制限事項として文書化（lim-*）

`lim-*` は「minigo がバインド外の stdlib ソースを実際に解釈しに行って init で落ちる」様子の観測で、fail までの深さ自体が信号になる。

- **lim-io / lim-csv / lim-bufio**: `io` の init が `sync.Pool` を要求 → 未バインド。更に `sync.Pool{New: fn}` は host type の複合リテラルフィールド初期化に対応していない（`cannot initialize host type with fields`）ので、単純なバインド追加では解けない。
- **lim-http / lim-template**: `sync.Map` 未バインド（net/http→godebug、text/template→reflect 経由）。
- **lim-flag**: `os.Args` 変数化で `len(os.Args)` は通るようになったが、`flag` 内部の `(*stringValue)(p)`（ポインタ→named 型変換）で trap。flag の実装は reflect 寄りで深い。
- **lim-sha**: `crypto/sha256` → `internal/goarch` の `4 << (^uintptr(0) >> 63)` — `^uintptr(0)` の結果を int64(-1) と見て `>>63` が算術シフトになり -1、`4 << -1` で `negative shift amount` panic。uint64 ドメインのシフトは符号付き近似のまま（B13-15 は値表現のみ）。
- **lim-yaml**: 外部モジュール `gopkg.in/yaml.v3` — go.mod 解決でソース自体は読み込めるが `reflect` の深度で断念。

## 3. 残っている近似・観測事項

- `errors.As` の型照合は葉名一致の近似（パッケージ違いの同名型を区別しない）— 前回同様。
- unsigned シフト（`x >> n` where x is uint64-family）は符号付きとして評価される（lim-sha が顕在化）。値表現と算術の整合は取れたが、シフトの型駆動 unsigned 化は未対応。
- `sync.Pool`/`sync.Map`/`bufio`/`encoding/csv`/`io.ReadAll`/`flag`/`text/template`/`net/http` 等のバインドは未実装 — init 依存連鎖のコストが高い。
- `os.Args` を func→var に変えたので、仮に `os.Args()` を呼んでいたスクリプトは壊れる（リポジトリ内の使用はなし、テストも通過）。
- `fmt.Fprintf` の writer 判定は fmtValue unwrap ベースの近似 — `io.Writer` を返す script 側実装はまだ拾えない。

## 4. 回帰テスト

`make format` / `make lint` / `make test` 全て緑。ユースケースコーパスは `~/usecasefuzz`（リポジトリ外）に残置。
