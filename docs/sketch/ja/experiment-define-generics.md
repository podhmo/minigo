# define パッケージのジェネリクス化 — 実験レポート（issue #13）

対象: `examples/convert-define/define` — [issue #13](https://github.com/podhmo/minigo/issues/13)「convert-define の define パッケージで generic メソッドを試す」
問い: (a) ジェネリクス版 API は組めるか、(b) go1.26 / go1.27 の分岐は必要か、(c) そもそも minigo はビルドタグを見てファイルを選べるのか。

結論: **組める**。分岐は go.mod の bump ではなく `//go:build go1.27` / `!go1.27` のファイル分割で実現し、minigo 側はビルドタグを正しく見ていた（実害ゼロ）。副産物として、明示的な型引数つき DSL 呼び出しが special form を素通りして静かに出力を失うバグを発見・修正した。convert-define fuzz コーパスは 23/23 で regress なし。

---

## 1. minigo はビルドタグを気にして読めるか — 読める

`resolve.ReadPackageFiles` が `go/build` の `MatchFile` で `//go:build` 制約（`go1.x` リリースタグ含む）、`_GOOS`/`_GOARCH` サフィックス、`_test.go` をフィルタする。評価されるリリースタグは**ツールチェーン側**（minigo を動かしている Go のバージョン）由来で、モジュールの `go` ディレクティブは見ない — これは `go list` と同じ選択結果になる。

実測（`dep` パッケージを `old.go`=`!go1.27` / `new.go`=`go1.27` に分割して minigo で `dep.Version()` を呼ぶ）: go1.27 ツールチェーン上の minigo は `new.go` 側を選び `"go1.27"` を返した。go/build と同じ意味論なので、DSL 側で「このファイルは読まれるか」を心配する必要はない。

補足: convert-define に関して言えば、そもそも `define` パッケージは**minigo に一度も読まれない**。`define.Convert`/`define.Rule` は呼び出し元ファイルの import 表から `SymbolID` を組み立てて `OpSpecialCall` に直行する（`trySpecial`）— decl の materialize すら起きない（plan_test の resolver spy で保証済み）。つまり「define をジェネリクスにしたら minigo がパースをコケるか」という懸念は構造上存在しない。引数の func literal は quoted AST としてハンドラが走査するだけで、内部の `c.Map(...)` 等も評価されない。

## 2. 1.26 / 1.27 の分岐 — 必要、そして実現できた

generic メソッドは **go1.27 の言語機能**。`go 1.26` の go.mod で書くと `-lang was set to go1.26` でコンパイルエラー。ただし分岐の実現手段は go.mod の bump ではなく**ファイル単位のビルド制約**:

- `//go:build go1.27` を付けたファイルは、(a) go1.27 未満のツールチェーンではビルド対象から外れ、(b) その**ファイルだけ** `-lang` が go1.27 に引き上げられる — モジュールが `go 1.26` のままでも generic メソッドがコンパイルできる（go1.27.1 + `go 1.26` go.mod で実測）。
- `//go:build !go1.27` のファイルに従来の `any` 版メソッドを置けば、go1.26 ツールチェーンではそちらが選ばれて普通にコンパイルできる（`GOTOOLCHAIN=go1.26.0` で実測: `methods_go127.go` は `[define.go methods_pre127.go]` に置き換わる）。

この2ファイル構成により「モジュールの `go` ディレクティブは据え置き・両ツールチェーンで同一 DSL ソースが書ける」という分岐が実現した。go.mod を 1.27 に上げる代替案もあるが、その場合 go1.26 ツールチェーンではモジュール自体がビルド不能になり（dep の `go` ディレクティブが呼び出し側を巻き上げる）、互換性を捨てることになる。

## 3. 実装した API

| シンボル | go1.27（`methods_go127.go`） | go1.26 以下（`methods_pre127.go`） |
|---|---|---|
| `define.Convert` | `Convert[Dst, Src any](func(c *Config, dst *Dst, src *Src))` | 同左（generic 関数は 1.18〜） |
| `define.Rule` | `Rule[Src, Dst any](func(ctx, *model.ErrorCollector, Src) Dst)` | 同左 |
| `c.Map` | `Map(dstField, srcField any)` — 共有ファイル側 | 同左 |
| `c.Convert` | `Convert[Dst, Src any](Dst, Src, func(ctx, *model.ErrorCollector, Src) Dst)` | `Convert(dst, src, conv any)` |
| `c.Compute` | `Compute[T any](T, T)` | `Compute(dst, expr any)` |

DSL の呼び出し形は一切変わらない（全て型推論で書ける）。generic メソッドを「呼ぶ」側に言語バージョンの制限はなく、go1.26 モジュールの DSL ファイルでも `c.Convert(dst.X, src.Y, fn)` は静的に型検査される（decl の `-lang` だけがゲート対象）。

設計上の判断:

- **`c.Map` は `any` のまま**。`Map[T]`（同一型要求）にすると `c.Map(dst.LineItems, src.Items)`（`[]SrcItem`→`[]DstItem`、登録ペア経由の要素毎変換 — e2e のフラッグシップ例そのもの）がコンパイルエラーになることが実測で判明。Map の契約「登録ルールで変換可能か」は型では表現不能なので、緩める方向（`Map[D, S any]` は何も検査しない = `any` と等価）も含め、静的検査の実がないと判断。
- **`c.Convert`/`c.Compute` は型で表現できる契約がある**ので generic 化の実がある。`c.Convert` は「変換関数が `(ctx, ec, Src) Dst` で、かつ Dst/Src がフィールド型と一致」をコンパイル時に強制する — 今まで `any` 受け取りで `define.Rule(badfn)` のような誤用が DSL-eval 時エラーだったものが、IDE 上で先に弾ける。
- **`define.Rule` も generic 化**（引数が関数シグネチャで絞れる）。副作用として `context` と `model` を import することになったが循環にはならない。

## 4. 途中で見つけて直したバグ: 明示的型引数で special form を素通り

ジェネリクス化すると DSL 側に `define.Convert[Dst, Src](...)` / `c.Convert[D, S](...)` / `c.Map[T](...)` という**明示的型引数つき呼び出し**が書けるようになる（合法 Go）。これが2箇所で噛み合わなかった:

1. `compile.call` の `trySpecial` は `x.Fun` が `*ast.SelectorExpr` の時だけ発火 → 型引数が付くと `IndexExpr`/`IndexListExpr` に包まれて素通りし、generic stub が `OpInstantiate`+`OpCall` で**空の本体を普通に実行** → conversion pair が一切登録されず、**エラーなし・exit 0 でほぼ空の generated.go** が出るサイレント破壊。
2. `mappingWalker.Visit` も同じく `call.Fun.(*ast.SelectorExpr)` マッチのみ → `c.Convert[D, S](...)` / `c.Map[T](...)` が静かにスキップされ、マッピング定義が消える。

どちらも「型引数を剥がしてからディスパッチ/マッチ」で修正（`IndexExpr`/`IndexListExpr`/`ParenExpr` を unwrap）。型引数は quoted ハンドラにとって無意味なので、unwrap して普通に処理するのが正しい挙動。修正後は明示インスタンス化つき DSL が正しく生成される（`internal/nested_test.go` の `TestParserExplicitTypeArgs`、`testdata/dslfile/defs.go` で pin）。

意図的に残した緩さ: `define.Convert[A, B](func(c, dst *X, src *Y))` のように**明示した型引数と literal の型が食い違う**呼び出しも受理される（本家 Go では型エラー）— minigo は DSL を型検査しないので受容側が広い。実害なし。

## 5. minigo 側のジェネリクス実力（確認のみ）

そもそも generic メソッドは既にインタプリタで動いていた（`Pair{}.Id(11)`、複数型パラメータ `Add2[T ~int, U ~int]`、generic 型上の generic メソッド `Box[T].Map[U]`、明示インスタンス化 `Id[int]`、全て正答）。`-lang` ゲートは存在しない — `go 1.26` モジュール内のスクリプトで generic メソッドを書いても minigo は実行する（本家は拒否）。これは「Go より受容が広い」 divergence で、ゲートするか設計として許容するかは TODO.md に起票した。

## 6. 検証

- `go build ./...` (go1.27 toolchain, `go 1.26` module): `methods_go127.go` が選ばれコンパイル。
- `GOTOOLCHAIN=go1.26.0 go build ./...`: `methods_pre127.go` が選ばれコンパイル。
- `go vet -tags codegen ./e2e_test/`（DSL の静的検査）: 両ツールチェーンで通過。
- `make format` / `make lint` / `make test`: clean。
- `make e2e`: `generated.go` を再生成して e2e PASS（生成物は変更なし）。
- convert-define fuzz コーパス（`podhmo/minigo-usecasefuzz`, `MINIGO_DIR` = 本 checkout）: **23/23 expected**（leaf-mismatch=BUILD-FAIL, neg*=GEN-FAIL を含む）。

## 7. 残件

- `go.mod` の言語バージョン未強制（§5）— TODO.md に `[ ]` で起票。
- `c.Map[T]` のような厳格版 Map を提供するかどうかは今後の論点（現状 `[]SrcItem`→`[]DstItem` を拒否してしまう過剰制約になるため不採用）。
- `define` パッケージが `context`/`model` を import するようになった点 — DSL 作者から見える依存が増えたが、stub なので実行コストなし。
