# 実験: oapi-codegen を minigo で動かす

対象は [oapi-codegen/oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) @ `43281d18a9d0`（kin-openapi v0.148.0、go.yaml.in/yaml/v3 v3.0.5、Go 1.27.1）。
目的は「minigo-usecasefuzz の grafana 系 realworld タスクと同じ形で、実在のコード生成ツールを minigo インタプリタ上で動かせるか」の確認と、そこまでの道筋・障害の洗い出し。

## 結論

- 第2ラウンドで、`examples/` の `go:generate` 行 **53 本すべて**が minigo 上で成功し、出力はネイティブとバイト一致した（詳細は後述）。

## 第1ラウンドの結論

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

## 第2ラウンド: examples 全体（53 本の `go:generate` 行）

`examples/` 配下の `go:generate go run .../cmd/oapi-codegen ARGS` 行をすべて、その行のディレクトリで実行した。`--src` の指定は第1ラウンドと同じ。各行の出力をネイティブのバイナリ（同じコミットから `go build` したもの）の出力と `diff -r` で比べた。ネイティブは 53/53 が成功し、出力はコミット済みファイルと一致する。

### 結果の推移

| 時点 | 成功（rc=0） | ネイティブとの出力差 |
|---|---|---|
| 第1ラウンド終了時 | 15/53 | — |
| 第2ラウンド（overlay 以外） | 52/53 | **なし**（成功した 52 本はすべてバイト一致） |
| overlay/api 完了後（全体を再実行） | **53/53** | **なし**（53 本すべてネイティブとバイト一致） |

実行時間は、53/53 を達成した最終実行で 1 本あたり平均 5.8 s（最大 7.6 s）、53 本の合計は 306 s。ネイティブは合計 6.8 s。

以下は、詰まっていた example ごとの章。どの修正でどの example が動くようになったかを記録する。第1ラウンドの修正で通った 15 本（only-models、extensions/* の大半など）は省略する。

### 章: goimports 経路の全般（多数の example）

- 症状: `cannot use map[string]bool as map[Symbol]bool`。x/tools の `imports` パッケージ内で起きる。
- 原因: パッケージレベルの非ジェネリック alias（`type Symbol = string` の類）が、composite の中では alias 名のまま綴られていた。そのため、同じ型を指す 2 つの綴りが別の型として扱われていた。
- 修正: `runtime: spell package-level aliases as their target inside composites`。ケースは `alias_elem_composite`。
- 付随: `math.Frexp` / `math.Modf` のバインド（`math_frexp_modf`）。
- 結果: クライアントやサーバのコードを生成する example（minimal-server/*、petstore-expanded/* など）の大半が、goimports を抜けるようになった。

### 章: authenticated-api / anyof-allof-oneof / callback（nil map）

- 症状: nil map への代入による panic。
- 原因: `maps.Clone` が `--src maps` 下で、本体の無い linkname 関数 `maps.clone` に落ちていた。そのため nil が返っていた。
- 修正: `asmimpl: implement the linknamed maps.clone`。runtime.Map を浅くコピーし、Named タグを保つ。ケースは `maps_clone_src`。
- 付随: difffuzz ケースに `SRC` ファイル（`--src` にするパッケージの一覧）を置けるようにした（`test: difffuzz cases may list --src packages in a SRC file`）。

### 章: webhook / petstore-expanded/chi / streaming/stdhttp/sse（compress/flate）

- 症状: `IndexRef` まわりの trap と `set field lastFreq`。生成物にファイルを埋め込む（gzip + base64 の swagger spec）example で起きる。
- 原因は 2 つ。
  - `&a[k]` の k が型付きの値のとき、ref のキーがアンラップされていなかった（`vm: unwrap typed index keys in &a[k] refs`、`addr_index_typed_key`）。
  - 配列への代入がストレージを差し替えていた。そのため、`(*[N]T)(s)` 経由のビューや、先に取ったポインタから変更が見えなかった（**SILENT**）。in place で上書きするようにした（`vm: array stores overwrite the array's storage in place`、`array_store_inplace` / `slice_to_arrayptr_store`）。
- 結果: この時点で webhook と petstore-expanded/chi がネイティブとバイト一致した。

### 章: output-options/preferskipoptionalpointer（alias 型フィールドの UnmarshalJSON）

- 症状: `error loading swagger spec: cannot unmarshal bool into field Schema.additionalProperties of type openapi3.AdditionalProperties`。
- 原因: kin-openapi の `type AdditionalProperties = BoolSchema` は alias。minireflect の `reflect.Type` が alias の typedef を保持したままだったので、`PointerTo(t).Implements(Unmarshaler)` が false になっていた。ソース版の encoding/json は `UnmarshalJSON` を呼ばずに bool を構造体へ入れようとした。
- 修正: `minireflect: alias types and alias-typed fields resolve to the target`。ケースは `reflect_alias_field_unmarshaler`（SRC あり）。
- 調べる途中で、別の不具合も見つかった。`fv.Addr().Interface()` が `*runtime.FieldRef` という **host 値**として VM に渡っていた。そのため、interface アサーションが host 側のメソッド集合（`Get` / `Set`）で判定されていた。`Set` という名前のメソッドだけ偶然通っていたので、原因の特定に時間がかかった。修正は `vm: host results that are field/index/deref refs stay script pointers`。ケースは `reflect_addr_interface_ref` と `reflect_addr_interface_alias`。

### 章: overlay/api（完了: バイト一致、6.2 s）

overlay は、OpenAPI Overlay（speakeasy-api/openapi-overlay）を gopkg.in/yaml.v3 の `yaml.Node` 上で適用する。障害を四つ順に越え、ネイティブの出力とバイト一致した。

1. `cannot convert []*minireflect.RValue to keyList`: yaml.v3 encoder の `keyList(in.MapKeys())`。host の `[]*RValue` が、要素型 `*minireflect.RValue` の slice として綴られていた（`%T` も違っていた）。要素を `reflect.Value` と綴るようにした（`vm: host []*RValue results spell their elements reflect.Value`、`reflect_keylist_sort` / `reflect_mapkeys_named_slice`）。
2. `cannot unmarshal !!map into yaml.Node`: 構造体フィールド `Update yaml.Node` の型が解決できず、穴の typedef（Kind invalid）になっていた。そのため yaml.v3 の `out.Type() == nodeType` が false だった。原因は、import path の末尾（`yaml.v3`）とパッケージ名（`yaml`）が違う場合に、型参照の解決が Scopes の basename キーしか見ていなかったこと。VM の式評価にはすでに「実名を materialize して探す」フォールバックがあり、それを型参照の解決にも入れた（`dispatch: qualified type refs find imports whose package name differs from the path`、`reflect_field_pkgname_differs`）。
3. `Token has no field or method Token`: speakeasy-api/jsonpath の `func (p *JSONPath) next(token token.Token)`。パラメータ名がパッケージ名と同じなので、prologue の型 coerce が `token.Token` をパラメータ値のフィールド選択として評価していた。Go はシグネチャの型を外側のスコープで解決する。そこで、パラメータ、名前付き結果、`return` の coerce に使う型式を、関数自身のスコープを隠した状態で評価するようにした（`compile: signature types resolve outside the function's own scope`、`param_shadows_pkg_field`）。
4. `cannot use []*yaml.Node as []*Node`: 2 と同じクラスで、今度は型の綴り（同一性の判定と `%T`）の側の問題。`typImportPath` / `importClauseName` も、パッケージ名と path 末尾が違う import を materialize して探すようにした（`runtime: type spellings find imports whose package name differs from the path`、`assign_pkgname_differs`）。

「import path の末尾とパッケージ名が違う」（gopkg.in/yaml.v3、`.vN` 接尾辞を持つもの全般）を引く経路は、VM の式評価、型参照の解決、型の綴りの 3 か所にあった。手当てされていたのは最初の 1 か所だけだった。`compile` 側の `Scopes[file][name]` 参照（compile.go の 2 か所）にも同じ前提が残っている可能性がある。

### 第2ラウンドで分かったこと

- 第1ラウンドの修正の多くは「一つの example を通すため」のものだったが、それだけで 15/53 まで通った。残りは少数の障害クラスに集中していた（alias の綴り、linkname、配列の in place 更新、reflect の境界）。修正 1 つで 10 本以上が同時に通ることが多かった。
- **成功した example では、出力の不一致が一度も出なかった。** 第1ラウンドで SILENT 系を潰したあとは、失敗はすべて「止まる」形で現れた。
- reflect の境界（host 値と script 値の受け渡し）は、`%T` の表示、interface の判定、named slice への変換が、それぞれ別の経路で壊れていた。境界で `goValueOf` を通る値の種類を一覧にして、まとめて点検する価値がある。

## 次のステップ

- minigo-usecasefuzz の realworld に `oapi-codegen-examples` タスクを追加する（targets.tsv にピン留め、`go generate` 相当の出力との diff を oracle にする）。
