# 実験: oapi-codegen を minigo で動かす

対象は [oapi-codegen/oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) @ `43281d18a9d0`（kin-openapi v0.148.0、go.yaml.in/yaml/v3 v3.0.5、Go 1.27.1）。
目的は「minigo-usecasefuzz の grafana 系 realworld タスクと同じ形で、実在のコード生成ツールを minigo インタプリタ上で動かせるか」の確認と、そこまでの道筋・障害の洗い出し。

## 結論（第1ラウンド時点）

- `examples/only-models` は minigo 上で最後まで動き、**`go run` の出力とバイト一致**した（ヘッダのバージョン行も含む）。
- そこまでの修正は minigo 側の 39 コミット（整形のみのものを含む）。障害ごとに `testdata/difffuzz/<slug>` の回帰ケースを付けた。
- yaml について: oapi-codegen が依存する `go.yaml.in/yaml/v3` も、kin-openapi が使う `github.com/oasdiff/yaml`（`oasdiff/yaml3` のラッパ）も pure Go で、cgo やアセンブリは無い。どちらもソース解釈で動いた。
- ただし実行には `--src` が必要（後述）。intrinsic のままでは足りない。
- 実行時間: minigo 約 5.1 s。ネイティブバイナリは 0.11 s、`go run`（warm）は 0.54 s、`go run`（使い捨て GOCACHE の cold）は 6.0 s。

## 実行方法

```sh
cd examples/only-models   # oapi-codegen の examples モジュール内
minigo run --src text/template,encoding/json,encoding/hex,encoding/base64,encoding/base32,slices,maps \
  github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -- -config cfg.yaml api.yaml
```

`go:generate` 行（`go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen ...`）をそのまま置き換えた形になる。

### `--src` が必要な理由

| パッケージ | intrinsic の不足 |
|---|---|
| `text/template` | `FuncMap` が無く、スクリプト側の関数を呼べない（oapi-codegen はテンプレート関数を大量に登録する） |
| `encoding/json` | 形だけで decode し、スクリプト側の `UnmarshalJSON` / `MarshalJSON` を呼ばない（kin-openapi はこれに全面的に依存）。ソース版は Go 1.27 の json/v2 実装になる |
| `encoding/hex`, `base64`, `base32` | json/v2 が使う `AppendEncode` などが未バインド |
| `slices`, `maps` | `Grow` などが未バインド |

bind されたパッケージにメンバが無いと `undefined` になる（メンバ単位でソースへフォールバックしない）。これは既存の TODO "Intrinsic coverage" の具体例で、oapi-codegen 用の項目を TODO.md に追加した。

## 到達の経過と見つかった障害

上から順に「詰まった場所 → 修正」。SILENT は「エラーにならず結果だけが間違う」もので、特に危険な種類。

1. **設定 yaml の読み込み（go.yaml.in/yaml/v3）**
   - lang gate が `x.f[k]` を `pkg.F[T]` と誤認した（go1.16 モジュール）。
   - 配列のスライスで要素型のパッケージ文脈が落ちた。
   - 宣言型間のポインタ変換を経たフィールド参照。
   - `string(reflect.StructTag)`。
   - 埋め込みフィールドのタグ欠落。
   - unexported な埋め込みの下の exported フィールドが Set できない。
   - `MethodByName` が同名フィールドを返した。
   - `&pkg.V` が imported global のセルを返さない。
   - `strings.ContainsFunc`、`reflect.Kind` / `ChanDir` の型バインド。
2. **ジェネリクス**
   - `~[]E` の制約チェック（バインド済み E の同一性、`any` の綴り）。
   - コア型からの型引数推論（`maps.Values(m)` 形）。
   - map / func 型オペランドのポインタ推論。
   - 部分的な明示インスタンス化（`slices.Grow[S](nil, n)`）。
3. **SILENT: 初期化に失敗したパッケージの埋め込み**: パッケージ init が失敗しても、その型を埋め込んだ型への interface アサーションが true になっていた（`TestInitFailEmbedAssert`）。
4. **SILENT: `make` / `append` の余剰容量**: 余剰容量がゼロ埋めされず、`s[:cap(s)]` で古い値が見えた（`reflect.Value.Grow` も同様）。
5. **alias**: alias を指すポインタの代入と、nil ポインタの代入。
6. **`//go:embed`**: oapi-codegen はテンプレートを `embed.FS` で持つ。`string` / `[]byte` / `embed.FS` を新規に実装した（`all:` 接頭辞、`.` / `_` の除外、ネストしたモジュールの除外に対応）。
7. **GOROOT 内部パッケージ**: `internal/bytealg`、`internal/stringslite` のスタブ。
8. **text/template（ソース）**
   - `runtime.Error` が `RuntimeError()` を要求していなかった。そのため `errRecover` が `ExecError` を runtime error と誤認して再 panic していた。
   - `any(v).(reflect.Value)` が失敗した。
   - `reflect.ValueOf(reflect.Value)` を `Call` に渡すと callee から `Kind()==ptr` に見えた。builtin の `len` が壊れていた。
   - `Value.Call` の nil 戻り値（error）が zero Value になり、`ret[1].IsNil()` が panic した。
9. **goimports（`golang.org/x/tools/imports.Process`）**: 生成の最後に `go env` を起動し、モジュールインデックスを読む。そのために次をバインドした。
   - 最小限の `syscall`（シグナルと errno）。
   - `unsafe.StringData` / `String` / `SliceData` / `Slice`（x/tools の event/label が文字列を詰めるのに使う）。
   - `os.File` 型、`os.Pipe`、`os.Interrupt`、`time.ParseInLocation`、`utf8.AppendRune`。
10. **SILENT: 終了コード 0 なのにモデルが生成されない**
    - 最初の原因: `type TBis T`（別の struct 型の上に定義した型）の `reflect.Type.NumField()` が 0 だった。kin-openapi の `T.UnmarshalJSON` は `type TBis T` に decode するので、ドキュメントのフィールドがすべて Extensions 側に流れていた。
    - 二つ目の原因: `v, ok := m[k]` で、k が untyped const だと ok=false になっていた。oasdiff/yaml の `extractOrigins` が `__origin__` を消せず、`IncludeOrigin` 有効時に spec の読み込みが壊れていた。
    - 三つ目の原因: `reflect.New(T)` のセルに型が付いていなかった。`*types = strs` で `Types` の同一性とメソッドが消えていた。
11. **ヘッダのバージョン行**: `runtime/debug.ReadBuildInfo` が ok=false だった。`go run` と同じく、main パッケージのパスとモジュールを返すようにした。cwd とは別のモジュールの main を動かす場合は、cwd のモジュールが require しているバージョンを使う（examples モジュールは replace 経由で `v2.0.0-00010101000000-000000000000` を返す）。

## 気づいたこと

- SILENT 系の障害は、どれも「終了コード 0 で出力が違う」形で現れた。realworld のように出力を diff する仕組みが無いと見逃す。
- minigo の時間の大半は goimports 経路にある（失敗した試行は goimports の手前まで 1.8 s 程度で進んでいた）。プロファイルはまだ取っていない。
- `go run` の cold（6.0 s）と同程度で、warm の `go run`（0.54 s）より約 10 倍遅い。今回の主眼である「go/packages 不要で動くか」には合格だが、「速い」の軸では現状不利。

## 残った課題（TODO.md に記録）

- `--src` 前提の intrinsic 不足（text/template の FuncMap、json の Unmarshaler、hex/base64/base32/slices/maps のメンバ）。
- bind 版の `utf8.EncodeRune` が Go と違うシグネチャ。
- minireflect の `embedRO` が `Elem` / `Index` を経由すると落ちる。
- 制約エラーの表示が `*ast.UnaryExpr` になる。
- `panic(err)` が `Error()` ではなく構造体の中身を表示する。

## 次のステップ

- examples 配下の残り 52 個の `go:generate` 行を一括で動かす。ネイティブでは 53/53 が成功し、コミット済みファイルと一致することを確認済み。
- minigo-usecasefuzz の realworld に `oapi-codegen-examples` タスクを追加する（targets.tsv にピン留め、`go generate` 相当の出力との diff を oracle にする）。
