# ユースケース差分ファジング実験 — バグ発見と修正のレポート

対象: `podhmo/minigo` の `main`（言語仕様回 `fuzz-language.md` がマージ済みの状態）
方法: 前回は言語仕様の網羅を狙ったが、今回は「Go の代表的なユースケースがそのまま動くか」を見る。実際に人が書く形のプログラム（テキスト処理・スクリプト用途を優先、バイナリ操作は低優先）を 27 本書き、`go run` を oracle として minigo の出力と diff する。差分が出たら潰す、のループ。加えて `lim-*` プローブで「バインド外パッケージに踏み込んだ時どう見えるか」を観測した。

ハーネス: [podhmo/minigo-usecasefuzz](https://github.com/podhmo/minigo-usecasefuzz)（`cases/<name>/main.go` + `run.sh`）。各ケースが使っている機能の一覧は同リポジトリの README.md を参照。`./run.sh` で再実行可能（`MINIGO_DIR` 未設定時は podhmo/minigo を隣に clone）。

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

`make format` / `make lint` / `make test` 全て緑。ユースケースコーパスは [podhmo/minigo-usecasefuzz](https://github.com/podhmo/minigo-usecasefuzz) に残置。

## 5. Round-2: PR #30 leftovers 解消 — 計画外の意思決定

§2 の lim-* 残件を解消した。8 件中 6 件 (lim-flag, lim-template, lim-bufio, lim-sha, lim-csv, lim-io) が PASS に、lim-http / lim-yaml は引き続き境界 TRAP。以下は計画時になかった設計判断。

### C1. host 呼び出しでの write-back / auto-pointer

`callReflectFunc` で slice 要素と ref 引数 (`argBack`/`sliceArg`/`refArg` + `backs`) を反映後に書き戻す。`toReflectValue` は ref ホルダ（Cell/FieldRef/IndexRef — 最後のもの勝ち）を追跡し、非 ref 戻り直前に auto-pointer 化 (`T → *T` または `ConvertibleTo(t.Elem())`) する。これで `csv.NewWriter(&sb)`・`io.Copy(&w, r)` のような `&x` 経由の writer 引数、`io.ReadFull` の slice への書き戻しが動く。Named の再ラップは sized-int の mask を保持する。

### C2. `any` 境界の deepHost — 例外として `chan any` は verbatim

`toReflectValue` の `any` パスは `deepHost` で Named/Slice/Map を完全に unwrap する（`any` 引数のホスト API には raw 値を渡す）。ただし `chan any` は `chan runtime.Value`（Value は `any` のエイリアス）なので、`OpSend`/`OpSelArm` は `toReflectValue` ではなく `chanSendValue` を使い、空 interface 要素型なら script 値を verbatim で送る。これで `ch <- m` した map が受信側で同一 `*runtime.Map` として返り、`got["k"]=8` が元 map を書き換える (ChanSliceSend/ChanMapSend リグレッションを防止)。`goValueOf` 側は逆方向に `[]any`/`map[any]any` を Slice/Map に復元するケースを追加し、ホスト側が本当にコンテナを返したケースを補う。

### C3. interface 充足の楽観判定 (methodSetOfU / MethodSetOfU / unsure)

embed された型が解決できない（vendor パッケージ、外部モジュール未解決、`*net.TCPConn` の peel 失敗など）場合、そのメソッド集合は不完全な可能性があるとして `unsure=true` を返す。充足チェックで「reqs に無いメソッドが見つからなかった」場合は `unsure` なら充足とみなす（未解決 embed がそのメソッドを持っているかも知れないため）。`Hooks.MethodSetOf` を追加し、Slice/Map/Chan に `Typ` を持つ宣言型 (declared slice/map/chan typedef — `type B []byte` のメソッド `b.M()`) もメソッド集合として返すようにした。これで `var _ closeWriter = (*net.TCPConn)(nil)` や `[]sniffSig{htmlSig(...)}` の interface チェックが通る。

副作用として、embedded 型が本当にメソッドを持たない場合でも誤って充足する（miss が発生し得る）が、メソッド呼出時に改めて trap するので実害は低い。

### C4. `(*T)(p)` ポインタ→named 型変換と OpSetInd/OpDeref

`convertPointer` で `*Declared` への変換は `Named{Typ: *T-td}` に包む（ElemOf が宣言型を返した場合のみ）。これは assignability (`var p PSq = &o` は依然として strict identity で trap) ではなく conversion のみに適用。`OpDeref` は pointer typedef を peel して pointee typedef で coerce し直すので `*sp` が `SV` タグで読める。`OpSetInd` は pointer typedef の elem で coerce してから `runtime.Unwrap(val)` で中身を剥がして格納 — 共有 cell (`*string` と `*stringValue` の二面性) はタグのない中立値を保持し、`*p` が string を、`*sp` が SV を返す。

`namedMember` は pointer typedef の peel を先頭で行い、peel 済みの typedef の value receiver は pointee の値を re-tag して bind する（`(*SV)(p).Set` が `*s` を通じて `SV` として書ける）。

### C5. unsigned domain の拡張

`sizedIntName` に `"uint"` を追加し、`unsignedName` でタグを判定して `binaryOp` は `uintOperand`/`uintBinOp`（ubox も含む）に分岐。`unaryOp` の `UnNeg`/`UnXor` も `*runtime.GoValue` uint64 に対応。シフトは `shiftOp` + `shiftCount` + `shiftInt`/`shiftUint`: count は Named-unsigned なら `uint64(iv)` でビット再解釈（`x << uint(-4)` = 0）、int64 <0 は `negative shift amount` panic、GoValue の unsigned kind は `.Uint()`。c≥64 では `<<`→0、`>>` →符号付きは符号ビット詰め・符号なしは 0。

### C6. bodiless func 宣言（`//go:linkname` stub / asm decl）の no-op 化

`compile.go` で `fn.Decl.Body == nil` の宣言は body を emit せず、各結果型に `OpNil + typeExpr + OpCoerceTop`（宣言型のゼロ値）だけを生成する。godebug.Setting.Value() 等が update() 未登録で空を返す実装と同じ結果になるため、stdlib の `//go:linkname` stub が init で死ななくなる。

### C7. `os.Args` の WithArgs/`--` パススルー

`Engine::args` + `WithArgs(argv)` を追加。`minigo run dir -- -x v` は `[dir, -x, v]` を `os.Args` として script に見せる（`--` 自体は CLI が食うので `go run . -- x` とは意味が違う点に注意 — Go 側は `--` が args に残り flag.Parse がそこで止まる）。既存の「`os.Args` は変数バインド」を維持。

### C8. `io` を native バインド化 — sentinel 同一性のため

解釈版 `io` パッケージは singleton `io.EOF` を与えられない（`err == io.EOF` が別 `*errorString` になり得る）ので、`io` は native バインドにし `errVal(io.EOF)` を返す。`eqlValue` は `*runtime.GoValue` を `.V` の同一性で比較するよう変更（nil 同士は等しい、uncomparable host 値は panic、`t.Comparable()` で判定）。これで `err == io.EOF` が成立する。

### C9. time の const + host type 追加

`time` バインドに layout consts（Layout/RFC3339 等）と `Time`/`Location` の hostType、UTC/Local GoValue を追加 — net/http init の `time.Time{}` / `time.RFC850` が解決するようになった。

### C10. 残件（境界）

- **lim-http**: `net` init が `netip` → `unique.Make` → `internal/abi.TypeFor` → `unsafe.Pointer` reinterpretation を要求。`reflect` init も `unsafe.Pointer` 必要。unsure 楽観判定で `(*net.TCPConn)(nil) as closeWriter` や `http2ResponseWriter` (vendor http2 embed) は通過したが、unsafe が関わる init 連鎖は実装上無理。**意図 TRAP として残す**。
- **lim-yaml**: `gopkg.in/yaml.v3` は外部モジュールで resolver が到達しない。意図 TRAP。
- **optimism の trade-off**: unsure で見逃す interface メソッド欠落はあり得るが、メソッド呼出時に trap するので誤 silent-fail はしない。
- **slice write-back**: `sliceArg` は `[]T` → `[]T` copy-back、`refArg` は `runtime.SetRef` 経由で中身を書き戻す（Named は mask 維持で再 wrap）。GoValue ポインタ leaf と `wb.rv` が同一なら skip。
- **deepHost**: Map は `Order`+`CanonicalKey` 順で `map[any]any` に、Slice は `[]any` に。TypedNil/IfaceNil/nil は `nil`。
- **`uint` を sizedIntName に追加**: `"int64"` は従来通りタグなし（変換で Named wrap しない）だが、`"uint"` はタグとして扱う — `%T` が `int` vs `uint64` を区別するため。
