# convert-define 網羅的ファジング実験 — バグ発見と修正のレポート

対象: `podhmo/minigo` の `examples/convert-define`
方法: 仕様上あり得る入力を「思考を頼りに」系統立てて投入する、手作りの PBT/fuzzing。オラクルは「生成コードが独立した Go モジュール内でコンパイルできるか」＋生成物の目視確認（生成は通るが意味が壊れているケースを拾うため）。
ブランチ: `devin/<ts>-convert-define-fuzz`（PR 参照）

---

## 1. ハーネス

各ケースを `~/convfuzz/cases/<name>/` に配置し、`run.sh` が一括実行する:

```
define.go (//go:build codegen, package gen) + source/ + destination/ (+ box/, a/, b/, c/, convutil/)
  → go run ./examples/convert-define -file <case>/define.go -output <case>/generated.go
  → ケース固有の go.mod (module example.com/m + replace で当該ワークツリーを指す)
  → go mod tidy && go build ./...
```

- **GEN-FAIL**: 変換ツール自身のエラー（パニックと想定内エラーを区別）
- **BUILD-FAIL**: 生成コードのコンパイルエラー = バグ検出の本命
- **OK でも目視**: コンパイルが通っても値が捨てられる・別型を呼ぶ等は別途検査

全42ケース。カテゴリ: ポインタ深さ・スライス/配列/マップ・名前付き型・同名型の別パッケージ・埋め込み・ジェネリクス・間接パッケージ・DSL の誤用・ルール関数の契約・自己パッケージ。

## 2. 発見したバグ（根本原因で分類、修正済み）

### A. `info.Structs` が裸の型名をキーにしていた

別パッケージの同名型（`a.User`/`b.User`）で dst の StructInfo が src のものに静かに上書きされる。`convertUserToUser(src *a.User) *a.User` という同一型恒等変換が出力されたり、field tag が src 側に書き込まれ dst 探索に残ったりした。**パッケージ修飾の正規キー `model.DeclKey`（`"import/path.Name"`）に変更**し、dst 側の非structも `handleConvert` で早期エラー化した。

### B. 発見ペアの StructInfo が生成されなかった

フィールド走査で見つかったペア（`Src.Sub`→`Dst.Sub`）は decl だけがワークリストに乗り、`info.Structs` は空のまま。生成時に「struct not found, skipping」と warn して関数本体だけを吐かない一方、呼出側は `convertSubToSub(...)` を出力して undefined 参照になった。**間接パッケージ利用（c15）や再帰型でも実害が出ていた**。`model.StructInfoFromDecl` を切り出し、generator 側でも必要に応じて実体化するようにした。

### C. ptr→ptr 専用コンバータ経路が2重に壊れていた

- **結果を捨てていた**: `convertSubToSub(ctx, ec, src.P)` が裸の文として出力され `dst.P` は常に nil のまま（c30）。
- **ポインタ深度を無視していた**: `Unref` が1段だけ剥がすのに fast path が任意の深さで発火し、`**Sub` フィールドに `*Sub` 引数の関数を呼んで型エラー（c01, c16 の `[]**T`, c17 の `map[K]**T`）。
- 修正: 深度ちょうど1かつ両側structの時だけ専用呼出（かつ `dst = ` 代入）。それ以外は汎用 nil チェック経路（`tmp := conv(*src); dst = &tmp` / 式モードなら即時関数）へ。

### D. 配列 `[N]T` がスライス扱い

`isSlice` が `ArrayType` を `Len` を見ずに拾っていた → `make([]T, ...)` を `[N]T` フィールドに代入してコンパイルエラー（c05）。`isArray`/`isSlice` を `Len == nil` で分離し、同型配列は `SameType` で直接代入、要素の異なる配列は `for i, item := range` で要素毎に変換する経路を追加。

### E. マップキーの変換が素通しだった

`declKeyOf(srcKT) != declKeyOf(dstKT)` でしか変換を発火せず、組込み型は両側 `""` で一致扱い → `map[int]X`→`map[int64]X` で `convertedMap[key]` の型不一致（c06）。**`SameType` 比較に変更**し、相違時は `generateConversion`（後述の leaf cast）で `int64(key)` や `destination.Key(key)` を出力（c34）。さらに map キーの struct ペアは `StructElemOf` が値側しか追わないため未発見 → `convertKeyToKey` 未定義（c41）。キー側も発見対象にした。

### F. 名前付き leaf 型が生代入

`source.Status`→`destination.Status`（基底は同じ string）や `int`→`int64`、`[]int`→`[]int64` の要素などが `dst = src` のまま出力され型エラー（c07, c26, c31 等）。`SameType` を先に評価して完全一致は代入、名前が違う leaf は `dstT(src)` キャストを出す（両側どちらかが非組込みの名前付き型、または両側が数値組込み型のとき）。

### G. 非公開フィールドを出力

`dst.token = src.token` — 生成コードは必ず別パッケージなのでアクセス不能（c09）。`token.IsExported` で `StructInfoFromDecl` 段階で除外。

### H. DSL 引数の未検証パニック

`strings.SplitN(dst, ".", 2)[1]` — `c.Map(dst.ID, src)` のような素な ident で index-out-of-range パニック（c12）。引数を「実際のパラメータ名をルートとするセレクタ」として検証する `fieldAccess` を導入し、誤用は DSL ソース位置付きのエラーにした（`c.Map`/`c.Convert`/`c.Compute` 共通）。

### I. dst フィールド名の typo が静かに無視

`c.Map(dst.Typo, src.X)` は tag 探索ミス → Priority 3 の正規化名マッチに落ち、違うフィールドに書くか沈黙（c28, c12b の `dst.Inner.ID`）。**明示マッピングの名前未解決はエラー**に変更。

### J. `define.Rule` のシグネチャ未検証

`func(t time.Time) string` を受け付けるのに呼出側は `f(ctx, ec, src)` → 引数過多でコンパイルエラー（c14）。**3引数 + 1戻り値 + `context.Context` + `*model.ErrorCollector` の契約を handleRule で検査**。なおリポジトリ内の testdata fixture も1引数で書かれており（`e2e_test/generated.go` が3引数で呼ぶ）、同じ潜伏バグが自作フィクスチャにも存在した。`c.Convert` の変換関数にも同じ契約チェックを（decl が解決できるとき）追加。

### K. `c.Convert` 変換関数のパッケージが `info.Imports` 未記録

`funcs.X` を `qualifyFunc` が解決できず warn（たまたま goimports が救うだけ）（c24）。`ctx.ResolveSymbol` で converter の import path を記録。

### L. 自己パッケージの import cycle

`info.PackagePath` はどこにもセットされておらず、生成対象パッケージ自身の型も `m "example.com/m"` を import して cycle（c36）。`Runner.Run` で define ファイルの **ディレクトリの実 import path**（`engine.Package(dir)` 経由 — `LoadFile` の合成パス `"<file>..."` ではない）をセット。

### M. 関数名の衝突

`a.User→b.User` と `a.User→c.User` で `convertUserToUser` が重複宣言（c08c）。ワークリスト確定後に一意化（`convertUserToUserFromaToc` のような決定的サフィックス → それでも衝突したら連番）し、`funcNamer` で呼出側と一致させる。

### N. `exprToString` の手書き再構成が狭すぎた

SelectorExpr/Ident/CallExpr しか対応せず `c.Compute(dst.F, src.A+" "+src.B)` やリテラルで unsupported エラー（c13, c13b）。**`go/printer` ベースに置き換え**、任意式をそのまま綴る。

## 3. 誤判定・限界として確認したもの

- `int`→`string`, `[]int`→`[]string` 要素の leaf 不一致（c08b, c26）は今もコンパイルエラーを出す。**string(int) はコンパイルが通るが意味が違う**ため、暗黙キャストより「ルールを書け」という loud failure を維持した。生成時警告の余地あり（TODO.md 記載）。
- `c.Map(dst.Inner.ID, ...)` / `c.Map(dst.X, src.In.V)` のネストしたパスは DSL の範囲外 → 今は早期エラー。
- `c.Map(dst.ID, src)`（src 裸 ident）はそもそも意味を持たない誤用 → エラー。
- `**T` が src 型として `func(c, dst, src **T)` の形は `StarExpr` 必須チェックで既に拒否（`Unref` しても `*T` で IsStructDecl が立たない）— 宣言方法の制約として正しい。
- `model.ConvertTag.Required`, `TypeRule.ValidatorFunc` は DSL から設定する経路が存在しない死んだ表面。

## 4. 試行錯誤で引っかかった点

- **ハーネスの自身のバグ**: ケース生成スクリプトで c11b の `source/`/`destination/` を `mkdir` し忘れ、GEN-FAIL を「変換器のバグ」と一瞬誤認した。オラクルを「GEN-FAIL のエラー種別」まで見るようにして以降は区別できた。
- **`pkg.Path` の罠**: `LoadFile` の返す package は `"<file>"+abs` という合成 Path で、`info.PackagePath` にそのまま入れると壊れる。ディレクトリを `engine.Package` で解決して初めて実 import path が得られる。
- **fixture 自身が感染者**: `testdata/plan|success/convutil` の1引数 `TimeToString` は「ルール関数は1引数でよい」という間違った前提をテストが追認していた。契約チェックを入れた途端テストが落ちて既存バグを炙り出した形。
- **`SameType` の位置**: ルール解決の直後・形状分岐の前に置くのが要点。`*T`→`*T` 同一型を専用呼出に流すと存在しない `convertTToT` を呼ぶので、先に短絡させる必要があった。
- **plan_test の laziness 検証**: `LocateDir` が1回増えるため spy の期待を更新（dir の実 path 解決は lazy loading の原則に反しない）。
- **SameType ショートカットの副作用**: 同一型なら `dst = src` としたところ、e2e ゴールデンで `*T`/`[]T`/`map[K]V` の代入が「新規コンテナへのコピー」から「ソースとのエイリアス」に変わり、生成差分として表面化した。値型に限り直代入、複合型はコピー経路に落とす形に絞った — ゴールデン比較がセマンティクス回帰の検出に効いた。

## 5. 振り返り: text/template 実装の扱いづらさ

実験を通じて感じた、現構成の辛いところ:

1. **「文モード」と「式モード」の二重経路**: `generateConversion` は `dst == ""` で「式を返す」、非空で「代入文ブロックを返す」を文字列連結で切り替える。ptr→value や value→ptr のように片側しか実装されていない経路が複数あり、ネスト位置（スライス要素・map 値・無名関数内）で初めて破綻が見える — c16/c17 がまさにそれ。`go/ast` で組み立てて最後に `printer.Fprint` する形ならこの二重性は要らない。
2. **ロジックがテンプレートと funcMap に分断**: `getAssignment` はテンプレート内の `{{ getAssignment ... }}` 呼出でしか走らず、関数名決定・import 登録・式生成が「テンプレート実行中の副作用」として起きる。関数名の一意化（M）のように全ペア確定後にしか決められない情報を呼出側へ渡すため `funcNamer` という外向きの状態が必要になった。
3. **生成位置の情報がない**: 壊れた出力を出したとき「どのテンプレ行のどのフィールドで」が戻ってこないので、デバッグは生成物とのにらめっこになる（今回は目視オラクルで補った）。
4. **goimports への暗黙依存**: `Imports()` マップが不完全でも `imports.Process` が最後に吸収するため（c24 の `funcs` import 欠落はたまたま救われていた）、「import 登録を忘れる」バグが表面化しにくい。
5. **文字列上の型名操作**: `getTypeName`/`sanitizeIdent`/修飾判定は全て「名前の綴り」を扱っており、`"int"` vs `"int64"` の比較といった semantic でないキーが多い — `SameType`/`CanonicalName` が inspect 層に育ったので今後はそちらへ寄せられる。

結論として、出力面は read しやすいコードが出る利点はあるが、**ロジック（型の形状判定・変換戦略）をテンプレートに近い文字列操作で書くこと自体が今回見つかったバグ群の大きな温床**だった。

#### AST 組み立て以外の選択肢（レビューでの質問への回答）

`go/ast` をフルで組むのは確かに重い。軽い代替案:

1. **戻り値の契約の統一（依存追加ゼロ）**: `generateConversion` を常に `(prelude []string, expr string)` を返す形にする。今の「`dst == ""` で式モード／非空で文モード」の二重性は、呼出側が `dst = expr` を出力するだけの形に退化させられる。`if src != nil` の分岐は prelude に逃がせるので、片側だけ実装が欠ける今回の構造的バグが起きにくい — まずこれが費用対効果最大。
2. **parse-then-compose**: 式は `parser.ParseExpr(fmt.Sprintf(...))` でノード化し、スケルトンだけ最小限の AST で組む。AST を手で積む辛さを式のパースに肩代わりさせる折衷案。
3. **printer 系ライブラリに乗る**: protobuf の `protogen` 風 `g.P(...)` や `dave/jennifer`（`Qual` が修飾名の衝突回避と import 収集を自動化）。関数名一意化や `Imports()` 漏れ（c24）が仕組みごと消える。example に依存を足すかは判断分かれ目。
4. `dave/dst` もあるがこの用途ではむしろ過剰かもしれない。

## 6. ケース一覧（実験後の最終状態）

| ケース | 狙い | 結果（修正後） |
|---|---|---|
| c00 baseline | ハッピーパス | OK |
| c01 ptrptr | `**Sub` フィールド | 修正（専用呼出を深度1に制限） |
| c02 ptrvalue | `*T`→`T` | 修正（式モード追加） |
| c03 ptrptr-mixed | `**T` と `*T` 混在 | OK |
| c04 ptrptr-basic | `**int` | 修正（nil チェック経路） |
| c05 array | `[N]T` | 修正（isArray 分離） |
| c06 mapkey | `map[int]`→`map[int64]` | 修正（SameType+cast） |
| c07 named | 別pkg同名型 `a.User`→`b.User` | 修正（DeclKey） |
| c08 samename | 同上 | OK |
| c08b samename2 | 同名型で leaf 不一致 | BUILD-FAIL（要 Rule — 仕様） |
| c08c funcname | 関数名衝突 | 修正（一意化） |
| c09 unexported | 非公開フィールド | 修正（除外） |
| c10 embedded | 埋め込み + `*T` フィールド | 修正（代入忘れ解消） |
| c11/c11b generic | `Box[T]`/`Box[T]`+sub | OK |
| c12 map-misuse | `c.Map(dst.ID, src)` | 早期エラー化 |
| c12b map-nested | `c.Map(dst.Inner.ID, src.ID)` | 早期エラー化（非対応） |
| c13/c13b compute | 二項式・リテラル | 修正（printer） |
| c14 rule-sig | `func(T)R` の Rule | 早期エラー化 |
| c15 indirect | 間接pkgのフィールド型 | 修正（実体化） |
| c16 slice-ptrptr | `[]**T` | 修正（式モード） |
| c17 map-ptrptr | `map[K]**T` | 修正 |
| c18 sametype | `shared.Meta` 共通型 | 修正 |
| c19 time | `time.Time` | OK（host  opaque） |
| c20 alias | `type A = B` | OK |
| c21 recursive | 再帰struct | 修正 |
| c22 chanfunc | `chan`/`func` フィールド | OK |
| c23 iface | `any`/`error` | OK |
| c24 funcimport | converter pkg import | 修正 |
| c25 nestedmap | 入れ子 map | OK |
| c26 slice-mismatch | `[]int`→`[]string` | BUILD-FAIL（要 Rule） |
| c28 map-typo | dst 名 typo | 早期エラー化 |
| c29 undiscovered | 未宣言の sub pair | 修正 |
| c30 ptr-field | `*T` フィールド | 修正（代入忘れ） |
| c31 slice-num | `[]int`→`[]int64` | 修正（cast） |
| c34 mapkey-named | 名前付きキー | 修正 |
| c36 selfpkg | 自pkg型 | 修正（PackagePath） |
| c37 src-nested | `src.In.V` | 早期エラー化 |
| c38 sliceslice | `[][]int` | OK |
| c39 empty | フィールド0件 | OK |
| c40/c41 mapkey-struct | struct キー（宣言有/無） | 修正（キー側発見） |

## 7. 第2ラウンド: #23（residuals 対応）に対する再探索

#23 で残件4件が実装されたため、同一ハーネスを `devin/1790827095-convert-define-fuzz-residuals` に向けて全ケースを再実行した（ハーネスは `REPO_DIR`/`GEN_DIR` を環境変数で差し替え可能に修正 — ハーネス自身も実験対象の修正に追随する必要があった）。

### 既存ケースの変化

- **c12b / c37（ネストパス）**: エラー → OK に。`dst.Inner.ID = src.ID`、`dst.V = src.In.V` が生成される。
- **c08b / c26（leaf 不一致）**: 依然 BUILD-FAIL だが、生成コードの doc comment と `slog` に `dst.ID: no conversion covers int -> string` の警告が載るようになった — 仕様どおりの loud failure＋診断。
- **c28（dst typo）**: エラー文が `mapped destination field "UserID" not found` から `field path "UserID": Dst has no field "UserID"` に。より良いメッセージ。
- その他の OK ケースは全て維持（回帰なし）。

### 新規ケース（#23 の新規面を狙う）

| ケース | 狙い | 結果 |
|---|---|---|
| c42-nested-ptr-dst | `c.Map(dst.PIn.Value, src.Name)`, `PIn *Inner` | OK — `if dst.PIn == nil { dst.PIn = &destination.Inner{} }` の nil 初期化が出る |
| c43-nested-ptr-src | `c.Map(dst.V, src.PIn.Value)`, `PIn *Inner` | OK — `if src.PIn != nil` ガードが出る |
| c44-nested-ptr-both | 両側 ptr 中間 + 同名フィールド | OK — 自動マップ `dst.PIn = convertInnerToInner(src.PIn)` の後にガード付き override |
| c45-nested-override | 自動マップされた子の中の葉を上書き | OK — `dst.Inner = *convert...(src.Inner)` の後に `dst.Inner.ID = src.OtherID`（override 勝ち） |
| c46-nested-deep | 3段パス `src.Mid.Leaf.V` | OK |
| c47-nested-nonstruct | 非 struct 中間 `src.A.B` (A is int) | 早期エラー `Src.A is int, not a selectable struct` |

全48ケース: OK 43、意図した DSL エラー 4（c12/c14/c28/c47）、警告付き BUILD-FAIL 2（c08b/c26 — 要 `define.Rule`）。新規の壊れは見つからなかった。

### 観察

- 明示マップは自動マッチの**後**に出力される設計で、`dst.PIn = convert...(src.PIn)`（子ごと変換）→ `dst.PIn.Value = src.X`（葉の上書き）の順になる。nil 中間の取り扱い（src 側はガードで沈黙 skip、dst 側は初期化して代入）は「nil なら書かない」という一貫した意味論。
- c44 で `src.PIn == nil` なら `dst.PIn` は nil のまま — 上書きも走らないので矛盾しない。
- 警告が doc comment にも残るのは生成物レビュー的に良い出力。

## 8. まとめ

ハッピーパス前提の指摘は概ね的中で、**「生成は成功するが生成物が壊れている」系のバグが14件の独立した根本原因に集約された**。特に「結果を捨てる」「定義しない関数を呼ぶ」「同名型の混線」の3系統は静かな正解コケ（silent corruption）であり、コンパイルオラクル＋目視の二段構えでないと拾えなかった。修正後は未対応の入力は全て DSL 側 or 生成時の明示エラーかコンパイルエラーとして「うるさく」失敗する状態になった。
