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

### P2-7c の adapter 移行（hostbridge/iface.go, adapt.go, scriptiface_gen.go）

`scriptIface` / `scriptFS` / `scriptFile` 系の script→host interface プロキシと
`adaptFunc` / `adaptFuncWith` / `adaptIface` / `funcMapPanicValue` を
`internal/hostbridge` へ移動した。

- `scriptIface` は `m *Marshal` フィールドを持ち、エラースロットの変換
  (`(m *Marshal) errOut` → `hostErr`) と `ScriptBytes` 高速パスを Marshal 経由で解決する。
- `Marshal` hook 追加: `AnchorCaller` / `AnchorMember`（vm の
  `callbackContext` / `callbackMemberContext` の注入口 — callback 呼び出し外の
  member 選択や marshal を idle anchor VM に振る）。`HostErr` の引数を
  `CallerRef` へ広げ MemberCaller 経路からも使えるようにした。
- `adaptOpts` は公開の `AdaptOpts` になり `FuncMapGuard` インターフェース
  （`Guard(callee)`）で vm の `*funcMapSig` を受け取る。`funcMapSig` 自体は
  `v.hostArgAsChecked` を使うため vm 側に残す（`guard` → `Guard` に公開化）。
- `scriptiface_gen.go` の生成先を `internal/hostbridge` に変更
  （`genifaces` の `out` と `//go:generate` ディレクティブを iface.go へ移動）。

### P2-7d の host 値操作（hostindex.go, hostslice.go, scalars.go, members.go, bounds.go）

`composite`/`convert`/`binop` で host 値を直接触っていた reflect 操作を
`internal/hostbridge` へ移動した。

- `hostindex.go`: `HostIndex`/`HostIndexInt`/`HostMapKey`/`HostSetIndex`/
  `HostElemRef`/`HostMapDelete`/`HostClear`/`HostSlice` は `*Marshal` メソッド。
  VM 側の trap・materialize・bound 判定は呼び出し側が組み立てる `IndexHooks`
  （`v.indexHooks(f)`）で注入する。
- `hostslice.go`: `UnboxHostSlice`/`StoreHostSlice`/`ReboxHostSlice`/
  `AppendHostSlice`/`CopyIntoHostSlice`/`HostSliceConv`/`HostSliceLiteral`。
  `vm.HostSliceOf` 系は公開 API のため薄い wrapper を残す。
- `scalars.go`: `HostFloat`/`HostScalarInts`/`HostInt64`/`HostUint`/`HostEnumConv`/
  `ConstToHostInt`/`UnboxGoValue`/`NamedBasicElem`/`HostNamedInt`/`HostStringOperand`/
  `HostString`/`HostStrOperand`/`HostNamedNonString`/`HostBoxIsNil`/
  `RawHostScalarOperands`/`UintOperand`/`BasicKeyPayload`/`HostNilEmbed`/`HostNilCallable`。
- `members.go`: `HostMemberInner`/`HostMethodInner`/`MethodInner`（host 値の
  フィールド/メソッド索引検索）。
- `bounds.go`: `BoundOperand`（公開フィールド I/U/Unsigned + `BoundOperandOf`
  構築子 + `Pos()`）と `IndexSmallInt`/`SliceBoundsReason`。vm の
  `boundOperand` は型エイリアス、`v.bounds`/`v.maxBound`/`v.boundUnsigned`/
  `v.boundOperand` は materialize 依存で vm に残す。
- vm に残るもの: typedef/同名性解決（`hostTypedefIs`/`twinPayloadEql`/
  `mapKeyOperand`/`coerceConcrete`/`convert`/`hostPtrConv`）、frame/サイト依存の
  dispatch（`selectMember`/`hostMember`/`namedMember`/`setField`/`setIndex`/
  `compositeOf`/`binaryOpFlags`/`eqlValue`/`ifaceEql`）。

### P2-7e: channel 操作（hostbridge/chanops.go）

- `ChanHooks`: VM 側の frame/proc 依存を注入する束。`VC`・`Trap`・
  `Park`（`v.park`）・`Done`（`chan struct{}`、proc 無しで nil）・
  `ZeroValue`（`v.zeroValue(f, et)`）・`ElemTypedef`・`Managed`
  （`p.ChanState` の managed 判定）・`Exit`（`procExit{}` を返す）。
  `v.chanHooks(f)` で呼び出し側が組み立てる。
- 移動したもの: `ChanOf`（`chanOf` — `*runtime.Chan`/`Cell`/`Named`/
  `IfaceNil`/`TypedNil`/`Nil`/`GoValue` の解決 + wake 計算）、
  `ChanParkWake`、`Marshal.ChanSendValue`（`chan any` は verbatim、
  それ以外は `m.ToReflect`）、`ChanSend`/`ChanRecvRV`（done arm 付きの
  ブロッキング select）、`ChanZero`、select-case 構築
  （`SelectCase` エイリアス + `SelectCaseSend/Recv/Default` +
  `Select`）、`RVOf`/`IsChan`/`IsMap`/`IsSlice`/`IsArray`/`IsString`/
  `NewElem`（reflect を名前で参照しないための helper）、
  `NilChanRV`/`NilChanValue`。
- vm 側の変更: `doneRV` → `doneChan() chan struct{}`（reflect を
  返さない）、`RegisterWakeChan(vc, ch any)`（内部で `RVOf`）、
  `mkIter` の ChRV/HostRV 構築は `RVOf`+kind helper、`runtime.SelArm.Case`・
  `runtime.Iterator.ChRV` フィールドの型（`reflect.SelectCase`・
  `reflect.Value`）は runtime 側のまま — vm は値を受け渡すだけで
  型名を書かないので import 不要。
- 残存 reflect（P2-7f で除去予定）: `ffi_marshal.go` の host 呼び出し、
  `generic.go` の intern、`const.go` の materialize、`assert.go`、
  `composite.go`/`binop.go`/`convert.go` の typedef 解決・dispatch、
  `panic.go`/`vm.go`/`call.go` の断片、テスト。
