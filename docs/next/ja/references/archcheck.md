# archcheck

[../TODO.md](../TODO.md) の M1-2、M1-3、M2-1、M2-2、M2-4、P2-7 から参照する。

## 方針

- 使うのは `go/parser` だけ（`parser.ImportsOnly` と、フィールド検査のための完全な parse）。`go/types`、`go/packages`、`go list` は使わない（AGENTS.md）。
- 規則はラチェットにする。最初は現在の状態を許可集合とし、フェーズごとに締める。許可リストの変更は、所有権の設計変更としてレビューする。
- 検査には必ず負例（違反を意図的に足すと落ちる）と正例を付ける。

## 現在の import グラフ（main `a3f4e24`、テストファイルを含む）

```text
.            -> bytecode compile index inspect internal/replhooks minireflect repl resolve runtime syntax vm
bytecode     -> syntax
compile      -> bytecode index runtime syntax
runtime      -> bytecode index syntax
vm           -> bytecode index minireflect runtime syntax
index        -> syntax
resolve      -> pkg/locator
minireflect  -> runtime
inspect      -> index resolve runtime syntax
repl         -> compile index internal/replhooks resolve runtime syntax
```

`vm → minireflect` は目標のグラフ（計画 §3）にない辺である。`minireflect` を `internal/hostbridge` 側に寄せるか、アダプタ越しにする必要がある。

## 段階的に足す規則

| いつ | 規則 |
|---|---|
| M1-2 | 現在のグラフを許可集合として固定する |
| M1-3 | 共有資産とそのキャッシュエントリのフィールド型を再帰的に検査する。`*runtime.Cell`、`*runtime.Function`、`*runtime.TypeDef`、`*runtime.ImportRef`、関数型のフィールドを禁止する |
| M2-1 | `internal/semantics` が `runtime`、`vm`、`compile` を import することを禁止する。可変のパッケージ変数と `init` も禁止する |
| M2-2 | `internal/semantics` から `EnsureReady*`/`EnsureConsts*` を呼ぶことを禁止する（selector 名で検査する。これで完全に証明できるわけではない） |
| M2-4 | `compile/` から `.Materialize()` を呼ぶことを禁止する |
| P2-7 | `vm/` が `reflect` を import することを禁止する |

import とセレクタ名の検査では、コールバック経由の実行が起きないことまでは証明できない。それは振る舞いテスト（M0-4）で補う。

## P2-7b の hook 注入（marshal.go の Marshal）

`toReflectValueDepth` とその配下（deepHost/hostMarshal、hostShapeOf、typedContainer、structDataHost ほか）が必要とする VM 依存の断片は、`hostbridge.Marshal` の hook フィールドとして注入する（`Materialize`、`ConstArg`、`AdaptFunc`、`AdaptFuncOpaque`、`AdaptIface`、`HostErr`、`ElemOf`、`MapKeyOf`、`AliasOf`、`HostTypOf`、`ScriptBytes`、`FuncMapElem`、`TemplateCallableNout`）。`ElemOf`/`MapKeyOf`/`AliasOf`/`HostErr` は `VMCaller` から `vmOf` で VM を取り直す closure になっていて、hostbridge 側は `runtime.VMCaller` だけを見る。

注意点: hook の実体（`adaptFunc`/`funcMapElem` など）は本体から `marshalHost` を参照するので、`var marshalHost = &hostbridge.Marshal{...}` と書くと var 初期化の依存閉路でコンパイルエラーになる。`func init()` で代入して閉路を断つ（init の本体は依存解析の対象外）。

`hostArgAs` はフレーム依存が強いので丸ごとは動かさず、`hostbridge.Lift(x, td, *ArgAsHooks, depth)` として切り出し、`v.hostArgAs` は呼び出しごとに hook を組み立てる薄い wrapper として残した。
