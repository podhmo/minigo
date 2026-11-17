# TypeID と Session ごとの intern

[../TODO.md](../TODO.md) の M4-1、M4-3〜M4-6 から参照する。

## キー

| 種類 | キー |
|---|---|
| source で宣言された named 型 | (ソース版, 宣言パッケージ, 宣言位置, 型引数) |
| bind された host 型 | *表している* 型の (PkgPath, Name, 型引数) |
| 無名型 | 構造的なキー（kind + 構成要素の TypeID） |

`reflect.Type` をキーにしてはいけない。反例は次のとおり。

- `atomicBox` は `atomic.Value`（intrinsics.go:2405）と atomic.Pointer 系（:2412）の両方を運んでおり、1 つのエンジンで 2 つの TypeDef を得ている。
- `timerChanBox` と `afterFuncTimer` は、どちらも `*time.Timer` を表している。

## host typedef を作っている箇所（すべて intern 表を通すようにする）

- 実行時: `vm.hostTypedefOf`（vm/vm.go:18295。呼び出しごとに新規作成し、named なら `HostNew`）、`hostPkgElemTypedef`（vm/vm.go:18347。新規作成し、`HostIdent`）
- bind 時: `HostNew`、`HostScalar`、`hostType`（intrinsics.go:3481）、`HostTypeOf`（native.go:208）
- ジェネリクスのインスタンス化によるコピー: dispatch.go:913、vm/vm.go:17276
- 既存のキャッシュは nil ポインタ用の `hostNilPtrTypes`/`hostNils`（vm/vm.go:18365/18388）だけ

## 計測（root スイート、エンジンとセッション 1,035 個）

- 99 個の host 型が、エンジンごとに新しい TypeDef を得ていた（bind 時の記録は計 112,805 件）。
- 1 つのエンジン内での重複:
  - bind 済みの TypeDef と合成した TypeDef の両方を持つ型が 14 個あった。例: `os.File`、`strings.Reader`、`regexp.Regexp`、`net.IP`、`net.TCPAddr`、`minireflect.RValue`（合成 78）、`runtime.Package`。
  - source 側で宣言された twin と合成した TypeDef の両方を持つ型が 26 個あった。例: `ast.Expr`（`hostPkgElemTypedef` で 163）、`ast.Ident`（88 と 34）、`ast.Stmt`、`ast.Field`、`fs.DirEntry`、`fs.PathError`、`fs.FileMode`、`httptest.ResponseRecorder`、`token.Token`、`url.Values`。

## helper の行き先

| helper（定義） | 呼び出し箇所 | 行き先 |
|---|---:|---|
| `hostTwinIs`（vm.go:15632） | 9 | Session 内ではポインタの `==` になる（M4-4） |
| `hostNilTwin`（vm.go:15573）+ `hostNilIdent` | 6 | 同上 |
| `sameSpelledTwin`（vm.go:15552）+ `hostElemTwin` | 5 | 同上 |
| `HostNamedIs`/`HostTwinOf`（runtime/value.go:2083） | 7 | intern のキーの計算そのものになる |
| `sourceTwinOf`（vm.go:18489） | 2 | 同上 |
| `peelAlias`（vm.go:16016、minireflect/rtype.go:69） | 22 | `TypeOf` の解決に含める |
| `tdShapeEq`（vm.go:16065） | 9 | `semantics.AssignableTo`/`Identical` |
| `underlyingShape`（vm.go:14348）/ `convShapeEq` | 2 | `semantics.ConvertibleTo` |
| `structFieldsEq`（vm.go:16145） | 3 | `semantics.Identical`（構造的な比較） |

値の変換層（`hostErrTwins`/`hostErrArg` intrinsics.go:8810/8841、`goNative`、プロキシのボックス）は残し、`internal/hostbridge` に移す。
