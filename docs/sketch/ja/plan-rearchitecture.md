# 計画: minigo のリアーキテクチャ（v1 以後の整理）

> **ステータス**: 提案。コードオーナーとしてパッケージ構成も VM の形も自由に変えてよい、という立場で書いた。ただしプロジェクトの根幹にある考え方は保つ。スナップショットは main `a3f4e24`（2026-11-17）。
>
> 英語版: [../plan-rearchitecture.md](../plan-rearchitecture.md)
> 関連: [../plan-minigo-vm.md](../plan-minigo-vm.md)（元の設計）、[todo-priority.md](./todo-priority.md)、[experiment-gosimilarity-refactoring.md](./experiment-gosimilarity-refactoring.md)、[../experiment-frame-path-perf.md](../experiment-frame-path-perf.md)

## 0. 変えてはいけないもの

以下はプロジェクトのアイデンティティそのものなので、以降のすべての段階をこれに照らして確認する。

| # | 不変条件 | 出典 |
|---|---|---|
| I1 | **Lazy Go**: パッケージは実行が到達したときに初めて locate、parse、index、initialize する。`go/types`、`go/packages`、`go list` は使わない | plan-minigo-vm §1, §4, §5、AGENTS.md |
| I2 | **全域コンパイラ**: コンパイラは AST を拒否しない。未対応の構文は `TRAP` にし、実行されたときだけ発火させる | plan-minigo-vm §3 |
| I3 | **スクリプトは valid Go**: スクリプトには gopls と vet が効く。インタプリタの拡張はスタブパッケージ（`minigo.dev/host`、`define`）で表す | plan-minigo-vm §1, §11 |
| I4 | **Quoted Go**: 特殊形式は canonical `SymbolID` で実行時にディスパッチする（`SPECIAL_CALL` + quote table）。AST→AST マクロにはせず、スタブパッケージもロードしない | plan-minigo-vm §12 |
| I5 | **成功したふりをしない**: 黙って誤答するくらいなら trap する | todo-priority.md |
| I6 | **速度は目標にしない**: 単純さとカバレッジを実行速度より優先する。性能改善は、コードの単純化を伴う場合か、計測済みの病理を取り除く場合に限る | README |
| I7 | **公開埋め込み API の source 互換を保つ**: `NewEngine`、`Run`、`Call`、`Bind`、`RegisterSpecial`、`WrapFunc`、`ValueOf`、`inspect`、`repl`、`storybook` の利用側（`examples/*` 全部）がそのままコンパイルできるか、機械的に移行できること | examples/ |

これ以外（パッケージ境界、VM の frame/stack レイアウト、型モデル、intrinsic テーブルの形）はすべて変えてよい。

## 1. 現状の診断

### 1.1 規模と形

| ファイル | 行数 | 中身 |
|---|---:|---|
| `vm/vm.go` | 19,093 | インタプリタループ、スケジューラ、host FFI、型なし定数、型アサーションと iface 充足判定、ジェネリクス推論、特殊形式の context |
| `intrinsics.go` | 11,033 | `installStdlib` だけで約 3,000 行（単一関数）。約 60 の stdlib パッケージを bind している |
| `compile/compile.go` | 7,295 | スコープ、文、式、定数畳み込み、静的型ヒント、goto 検査、複合リテラル、特殊形式 |
| `runtime/value.go` + `typedef.go` + `package.go` | 5,662 | 値、`TypeDef`、パッケージのライフサイクル、`SpecialContext`、`VMCaller`、panic/trap。すべて一つのパッケージにある |
| `minireflect/` | 6,766 | スクリプト値用の reflect ファサード |
| root `minigo` | 約 18,000 | Engine、builtin、dispatch（メソッドセット）、inspect、iter.Seq、host vars、embed、intrinsic |

元のレイアウト（plan-minigo-vm §14 の `loader/`、`types/`、`builtin/`、`intrinsic/`、`special/`、`ffi/`、`debug/`）は実体化しなかった。それらの責務は `vm.go`、`runtime/`、root パッケージに溜まっている。

### 1.2 構造上の問題（コストの大きい順）

1. **型モデルが一つではない。** `runtime.TypeDef` は「遅延 AST 参照」（`Spec`、`Anon`、`EmbedSpecs`、`IEmbeds`、`TConstraints` が `ast.Expr`）と「解決済みの型」（`Elem`、`Embeds`、`Methods`、`Binds`、`HostIdent`、`HostNew`、`HostScalar`）を兼ねている。そのため型の同一性は、VM に散らばった多数のヒューリスティクスで近似されている。`tdShapeEq`、`sameSpelledTwin`、`hostNilTwin`、`hostTwinIs`、`peelAlias`、`structFieldsEq`、`underlyingShape`、`typeExprFor`、`reflectTypeExpr` などがそれにあたる。gosimilarity の調査では、「`td.Anon` が nil なら `td.Spec.Type`」という慣用句が 18 箇所に、TypeDef の構築が 4 箇所に重複していた。TODO.md の正しさに関するバグの多く（named と host の twin、alias の同一性、host embed の promote フィールド）がこの継ぎ目にある。
2. **静的型の知識がコンパイラと VM に分かれている。** コンパイラは場当たり的に部分的な静的型を求め（`staticOpTyp`、`callOpTyp`、`stampVarTyps`、`opTyp`）、それを `OpLocalTyp`/`OpGlobalTyp` のスタンプと呼び出しごとの `statics []*TypeDef`（vm.go で 50 箇所）として VM に渡している。VM はそれを推論と型なし定数のデフォルト化に使う。コンパイラが「何を知ってよいか」という契約には名前すら付いていない。
3. **コンパイル時の遅延性の契約が暗黙のままになっている。** コンパイラは `pkg.Name` のようなオペランドの型を求めるために `ImportRef.Materialize()` を呼ぶ（`compile/` と `vm/` で 14 箇所）。そのため、コンパイル中の関数のどこかで参照されたパッケージは、通らない分岐の中の参照であっても parse と index まで進む。initialize はしないので I1 の精神は守られている。しかし「コンパイルは *Indexed* まで進めてよく、*Initializing* には進めない」というルールはどこにも書かれておらず、強制する仕組みもない。
4. **VM が god object になっている。** `*VM` は次のものをすべて抱えている。
   - dispatch ループ、frame、defer/panic/recover
   - goroutine のスケジューリングとデッドロック検出（`proc`、`gwait`、`parkWake`）
   - reflect 境界（`toReflectValueDepth`、`goValueOf`、`hostMarshal`、`adaptFunc`）
   - script→host のインタフェースアダプタ（`io.Reader`、`fs.FS`、`sort.Interface` などを実装する `scriptIface`）
   - 型なし定数の演算（`UConst`、`constToBasic`、`scalarConst`）
   - インタフェース充足判定
   - ジェネリクス推論（`inferCached`、`unifyIndices`、`checkTArgs`）
   - `specialCtx`

   これらはどれも独立した関心事で、テストもそれぞれ別の場所にある。
5. **`VMCaller` が 21 メソッドのインタフェースになっている。** builtin と intrinsic はすべてこれに依存している。そのため、どれか一つを root パッケージや `vm` の外へ出そうとすると、`VMCaller` ごと引きずることになる。
6. **intrinsic テーブルがパッケージ単位の bind に戻っている。** これは plan-minigo-vm §11 がなくそうとしていたものそのものである。実務上は正しい判断だった（experiment-oapi-codegen-perf によれば「~7% を超えて効くレバーは native 実行だけ」）。しかし今の形は 3,000 行の単一関数で、パッケージごとの所有者もテストもない。`minigo gen-intrinsics` があるのに、生成されたファイルも一つもない。
7. **frame とローカル変数のレイアウトが構造上悲観的になっている。** ローカル変数はすべてヒープ上の `*Cell` になる。ストア先には毎回 `OpLocalRef` が出るので、ほぼすべての chunk で `chunkCellsEscape` が true になり、cell ブロックの再利用（#1024）がめったに効かない。operand stack も frame ごとに伸びていく。これは *設計* の問題であり（コンパイラがスロットを分類していない）、性能系の TODO はその症状にすぎない。

## 2. 目標アーキテクチャ

### 2.1 レイヤ

```text
                ┌──────────────────────────────────────────────┐
  consumers     │ cmd/minigo  repl  storybook  inspect  examples│
                └───────────────────────┬──────────────────────┘
  facade        minigo (Engine, Option, Run/Call/Bind/RegisterSpecial,
                        WrapFunc/ValueOf — 薄く、再エクスポート)
                                        │
  host side     stdlib/…  (bind されたパッケージ)   special/  (レジストリ,
                hostffi/  (reflect 境界,             QuotedCall, Context 実装)
                           script→host アダプタ)
                builtin/  (len, append, make, …)
                                        │
  execution     vm/        (dispatch ループ, frame, call, defer/panic)
                vm/sched/  (goroutine, park/wake, デッドロック検出)
                                        │
  compile       compile/   (AST → bytecode; スロット分類;
                            types.Oracle 経由の静的ヒント)
                bytecode/  (op, Chunk, CallSite 記述子, verifier, disasm)
                                        │
  semantics     types/     (Type: 解決済み + intern; Ref: 遅延;
                            メソッドセット, iface 充足, 変換規則,
                            ジェネリクス推論/インスタンス化)
                constant/  (型なし定数: UConst 演算, デフォルト化)
                                        │
  object model  runtime/   (データのみ: Value の種類, Cell, Struct, Slice, Map,
                            Chan, Function, Closure, Panic, Trap)
                                        │
  front         loader/    (パッケージライフサイクルの状態機械, ImportRef, Env)
                index/  syntax/  resolve/  pkg/locator
```

ルール:

- **import は下向きのみ。** `archtest`（§4 フェーズ 0）で強制する。
- **`runtime/` はデータを持ち、アルゴリズムを持たない。** 構造体定義と自明なアクセサは残す。Go は import の循環を許さないので、`Named → TypeDef → Function → Chunk` は同じパッケージに置く必要がある。解決、充足判定、推論、変換のロジックは、上位の `types/` と `constant/` に移す。
- **`vm/` は `reflect` を知らない。** ループから見た host 値は不透明な `*runtime.GoValue` であり、host とのやり取りはすべて、VM の構築時に渡される `hostffi` インタフェースを通す。

### 2.2 `VMCaller` を capability インタフェースに分割する

21 メソッドを小さなインタフェースに分ける。VM がすべてを実装し、利用側は必要最小限のものだけを受け取る。

```go
package runtime

type Caller interface   { Call(fn Value, args []Value) (Value, error); Recover() Value }
type Spawner interface  { Spawn(fn Value, args []Value) *Task; Task() *Task }
type TypeOps interface  { TypeOf(Value) *TypeDef; Zero(*TypeDef) Value; Convert(*TypeDef, Value) (Value, error); Equal(*TypeDef, Value, Value) bool; Copy(Value) Value }
type Members interface  { Member(base Value, name string) (Value, bool); MethodSetOf(Value) (map[string]bool, bool) }
type Frames interface   { CallerPCs() []uintptr; CallerFrame(pc uintptr) (CallSite, bool) }

// 移行中の source 互換のため、VMCaller はこれらの union として残す。
type VMCaller interface { Caller; Spawner; TypeOps; Members; Frames; /* … */ }
```

こうすると `strings` の intrinsic が必要とするのはせいぜい `Caller` だけで、大半は何も要らない。これが bind を root パッケージから外へ出す前提条件になる。

### 2.3 型モデルを一つにする: `types.Type` + `types.Ref`

```go
package types

// Ref はレキシカルな文脈付きの、未解決で遅延な型式。
// インデックス作成時に生成され、作るために何もロードしない。
type Ref struct { Expr ast.Expr; Scope *Scope }   // Scope: ファイルの import + ローカル型 + ジェネリクス束縛

// Type は解決済みの型。Universe ごとに intern される。同一性はポインタ等価で
// 判定するので、*Twin/*Shape 系のヒューリスティクスはすべて == に置き換わる。
type Type struct {
    Kind    Kind
    Named   *NamedInfo   // パッケージパス + 名前 + 型引数（無名型なら nil）
    Elem    *Type; Key *Type; Len int64
    Fields  []Field      // 遅延かつ一度だけ解決（埋め込みパスも事前計算）
    Methods *MethodSet   // 遅延かつ一度だけ構築。アサーションと dispatch で共有
    Host    reflect.Type // host の型そのものであれば非 nil
    Def     *runtime.TypeDef // 移行中の逆参照
}

type Oracle interface {
    // Resolve はパッケージを Indexed まで進めてよいが、Initializing には進めない。
    Resolve(Ref) (*Type, error)
}
```

- host 型には `reflect.Type` 1 つにつき `*Type` を 1 つだけ割り当てる。bind された named 型（`time.Duration`）とその source 側の宣言も *同じ* `*Type` に解決させる。twin のチェックを増やすのではなく、twin という概念そのものをなくす。
- `MethodSet` は型ごとに一度だけ計算し、`MethodSetOf`、`MethodFuncsOf`、インタフェース充足判定、promote されたメンバの検索のすべてで使う。これで「メソッドセットを二度歩く」TODO が構造的に解消する。
- selector 解決では、`(Type, name)` ごとに *フィールドインデックスのパス* をキャッシュする。これで「selector 解決が名前で探し直す」TODO が解消する。
- `runtime.TypeDef` は宣言レコード（index と compile が作るもの）として残し、`TypeDef.Type()` が intern 済みの `*Type` を遅延で返すようにする。移行は呼び出し箇所ごとに進め、一斉切り替えはしない。

### 2.4 コンパイル時の遅延性の契約を明文化する

> **C1.** 関数をコンパイルするとき、その関数内で *構文上* 参照されているパッケージは `Indexed` まで進めてよい。`Initialize` は決して実行しない。
>
> **C2.** そうしたパッケージの読み込みは、コンパイラが所有する単一の `types.Oracle` を必ず経由する。`compile/` から `ImportRef.Materialize()` を直接呼んではならない。
>
> **C3.** C1 に違反しないと答えられない場合や解決に失敗した場合、ヒントは *unknown* とし、VM が動的に判断する。ヒントが欠けたことで、gc ならしない形で意味が変わってはならない（I5）。unknown は常に安全である。

これで `compile/typehint.go` が `staticOpTyp`、`callOpTyp`、`opTyp`、`stampVarTyps` を置き換える。その出力は chunk に格納される命令ごとの静的情報（2.5 参照）とし、`OpLocalTyp` のプッシュや呼び出しごとの `statics` スライスといった裏ルートは使わない。

オプトインの `WithStrictLazyCompile()` を用意し、テストで C3 を実証できるようにする（このモードの oracle は、すでに `Indexed` のもの以外には答えない）。このモードで conformance スイートを走らせても結果が同一であることを確かめる。違うのは、遅くなるか、動的チェックが増えるかだけでなければならない。

### 2.5 VM の形

以下はそれぞれ独立して入れられ、入れるたびに計測する。

1. **コンパイル時にスロットを分類する。** 各ローカル変数を `Plain`（frame のスロット配列に値を直接置く）か `Boxed`（`*Cell`）に分ける。`Boxed` になるのは、クロージャにキャプチャされる場合、`&x` でアドレスを取られる場合、特殊形式の `Eval` スコープに公開される場合である。ストア先には、`OpLocalRef` と汎用 ref ストアの組み合わせではなく直接の op（`OpSetLocal`、`OpSetLocalIndex`、`OpSetLocalField`）を使う。これで非引数ローカルの二重アロケーションがなくなり、`chunkCellsEscape` が正確になり、`assignRef` が *単純になる*。
2. **goroutine ごとに連続した値スタックを 1 本持つ。** frame はその中の窓 `(base, sp)` とし、`Chunk.MaxStack` は bytecode verifier が求める。引数はその場で渡す（`popArgs` のスライスも、bound method でレシーバを先頭に挿入するためのコピーも要らなくなる）。
3. **chunk に `CallSite` 記述子を持たせる。** 静的な引数 typedef、spread 要素の typedef、特殊形式の quote インデックス、ソース位置を、1 つの不変な `bytecode.CallSite` にまとめ、`OpCall` のオペランドから参照する。`frame.argbuf/statbuf` と `deferredCall.statics/spreadTd` は不要になる。
4. **`CallSite`/`Site` のスロットにインラインキャッシュを置く。** atomic pointer にするので、共有 chunk でも race-free のままになる。`OpGlobal` は解決済みの cell をキャッシュし、`:pin` や再 bind で進む `Env` epoch でガードする。`OpSelect` は `(*Type → アクセスパス)` をキャッシュする。これは plan-minigo-vm §16 にある「後で入れる `OpSelect` インラインキャッシュ」にあたり、型が intern されたことで初めて可能になる。
5. **bytecode verifier**（`bytecode.Verify`）を、テスト時とデバッグオプション指定時に走らせる。スタック効果の整合性、ジャンプ先、スロット種別を検査する。これでコンパイラのバグの一部がその場でテスト失敗として現れるようになり、`MaxStack` も得られる。
6. **維持するもの**: boxed コンポジットを使う `Value = any`、多値返却の `Tuple`、`Trap` と `Panic` の区別、関数単位の遅延コンパイル、`SPECIAL_CALL`。tagged な `Value` 構造体は明示的に **スコープ外** とする（I6。プロファイルはフラットで、効果が実証されたレバーは native な bind である）。
7. **`OpEvalAST` を廃止する。** ただし REPL/`EvalExpr` のブリッジとしての用途は残す。v1 からの移行という役目は終わったので、`OpEvalFragment` に改名し、ブリッジ用の op として文書化する。

### 2.6 host 境界をパッケージにする: `hostffi/`

`vm.go` から次のものを移す（現状のおおよそ 3,950〜8,600 行目と 12,000〜14,400 行目）。

- `goValueOf`、`toReflectValueDepth`、`hostMarshal`、`adaptFunc`、`hostArgAs`
- opaque store、host の nil/twin ヘルパ
- `scriptIface` 一族（`io.Reader`、`io.Writer`、`io.Seeker`、`fs.FS`、`fs.File`、`fs.FileInfo`、`fs.DirEntry`、`sort.Interface`、`fmt.Stringer` など）

アダプタ一族は、組み合わせごとの手書き（`scriptFileSeek`、`scriptFileSeekWT`、`scriptDirFileSeekWT` などの組み合わせ爆発）をやめ、テーブル（インタフェース → メソッド → arity）から生成する。

### 2.7 bind パッケージを `stdlib/<path>` にする

```text
stdlib/
  registry.go          Provider: import path → 遅延構築される *runtime.Package
  bind/                ヘルパ（fn, fnvc, fnvcRaw, hostType, …）— 今の hostHelpers
  strings/  bytes/  strconv/  fmt/  time/  os/  encoding/json/  text/template/ …
```

- 各サブパッケージは `func Install(r *bind.Registry)` を公開する。パッケージはスクリプトが最初に import したときに初めて *構築* する。今は全エンジン（とセッション）が `installStdlib` 全体を最初に払っていて、これは I1 の精神に静かに反している。
- 純粋なシグネチャだけの bind（`strings`、`strconv`、`math`、`unicode` の大半）は `minigo gen-intrinsics` で生成し、`// Code generated` ヘッダ付きでチェックインする。手書きのファイルにはアダプタ（コールバック、チャネル、タイマー、`FuncMap`、`sync.*` のボックス）だけを残す。
- `--src` が通るものについては、bind した各パッケージに、source 解釈版との parity テストを付ける（difffuzz の判定契約を使う）。bind がずれたときに黙って誤答するのを防ぐガードになる（I5）。
- `WithAllowedRoots`/`WithHostPolicy` は、個々の bind の中でチェックするのをやめ、レジストリへのフィルタにする。

### 2.8 特殊形式を `special/` にする

`runtime.SymbolID`、`QuotedCall`、`SpecialFunc`、`SpecialContext` と `vm.specialCtx` を `special/` に移す。`SpecialContext.ResolveType` は `types.Ref`/`*types.Type` を返すようにする。これで convert-define に、plan-minigo-vm §12.5 が描いた API を実際に提供できる。登録は引き続き `Engine.RegisterSpecial` で行う（I7）。コンパイラが参照するのはレジストリの `Has(SymbolID)` だけにする（I4: スタブパッケージは決してロードしない）。

### 2.9 root パッケージはファサードになる

`minigo.go` には `Engine`、オプション、公開 API を残し、それ以外を移す。

| 現在 | 移動先 |
|---|---|
| `builtins.go` | `builtin/` |
| `dispatch.go`（メソッドセット、埋め込み、iface） | `types/` |
| `intrinsics.go`、`hostvars.go`、`hostslice.go`、`asmimpl.go` | `stdlib/…` |
| `iterseq.go` | `hostffi/iterseq.go` |
| `inspect.go` | `inspect/`（bind 側） |
| `embed.go`、`parse.go`、`parsecache.go` | `loader/` |
| `SourceOf`/`LoadFile`/`LoadDirScope` の内部 | `loader/` |
| `vet.go` | `cmd/minigo/vet` または `vet/` |

root の型エイリアスで I7 を保つ（`type Value = runtime.Value` など。examples がすでにこの形で import している）。

## 3. パッケージ間依存の目標

```text
syntax, resolve, pkg/locator           (互いのほかに内部依存なし)
index        → syntax
runtime      → syntax, bytecode(Chunk のみ)        [データ]
loader       → resolve, syntax, index, runtime
constant     → runtime
types        → runtime, constant, loader(Indexed 専用 API)
bytecode     → syntax
compile      → bytecode, types(Oracle), runtime, special(レジストリの Has のみ)
vm/sched     → runtime
vm           → bytecode, runtime, types, constant, vm/sched, hostffi(インタフェース)
hostffi      → runtime, types, constant
builtin      → runtime (capability インタフェース)
special      → runtime, types
stdlib/...   → stdlib/bind → runtime (capability インタフェース), hostffi
minigo       → 上記すべて
inspect, repl, storybook, cmd → minigo
```

`loader` は `types` に対して `Indexed(path) (*Package, error)` という狭いビューだけを公開する。これで型レイヤは `Initialize` を *呼べなくなり*、C1 がレビューではなく型システムで強制される。

## 4. 移行計画

各フェーズは振る舞いを保存する。それぞれを小さな PR の列として入れ、`make format lint test`、`make test-race`、pin 済みの difffuzz ケース、examples をすべて通す。フェーズ 1〜2 は機械的な作業で、3〜4 で設計変更を行う。

### フェーズ 0 — ガードレール（小さく、最初にやる）

- `archtest_test.go`: 各パッケージの import を `parser.ParseFile(…, parser.ImportsOnly)` で読み（`go list` は使わない）、§3 の表を満たすことを確かめる。最初は現在のグラフを許可集合とし、フェーズごとに締めていく（ラチェット）。
- ファイルサイズのラチェット: 生成ファイルでもテストでもない `.go` ファイルが記録済みのサイズを超えたら失敗するテスト（予算は縮む一方）。
- ベースラインを記録する: `session_bench_test`、`genericbench_test`、realworld の `grafana-openapi` の実行時間とアロケーションバイト数。以降のフェーズではすべて差分を報告し、10% を超える劣化には PR で理由を書く。
- `bytecode.Disasm` の golden テストセット（代表的な関数を十数個）を追加する。コンパイラのリファクタリングの影響がテキストの diff として見えるようになる。

### フェーズ 1 — ファイル分割のみ、パッケージは動かさない（機械的）

- `vm/vm.go` を、既存の `// ----` セクションコメントに沿って次のファイルに分ける: `loop.go`、`frame.go`、`call.go`、`panic.go`、`trace.go`、`sched.go`、`ffi_marshal.go`、`ffi_iface.go`、`const.go`、`binop.go`、`convert.go`、`assert.go`、`generic.go`、`special.go`、`select.go`、`composite.go`、`iter.go`。
- `compile/compile.go` を次のファイルに分ける: `scope.go`、`stmt.go`、`assign.go`、`expr.go`、`call.go`、`lit.go`、`constfold.go`、`typehint.go`、`goto.go`、`special.go`。
- `intrinsics.go` を bind する import path ごとに 1 ファイルへ分ける。`installStdlib` は `installStrings(h)`、`installTime(h)` … の呼び出しを並べるだけの関数にする。
- `git diff --color-moved=dimmed-zebra` でレビューする。ロジックの変更はゼロ。
- scratch worktree で検証済みの gosimilarity R1〜R6 の統合（正味 −452 行）もここで取り込む。`(*TypeDef).TypeExpr()` もこれに含まれる。

### フェーズ 2 — 既存の継ぎ目に沿ってパッケージを抽出する

各ステップのインタフェースが狭くなるように、次の順で進める。

1. capability インタフェース（§2.2）。`VMCaller` はその union にする。
2. `vm/sched`（goroutine、park/wake、デッドロック検出）。ループとは `spawn`/`park`/`unpark` を通してしかやり取りしない。
3. `special/`（§2.8）。
4. `constant/`（UConst の演算とデフォルト化）。
5. `hostffi/`（§2.6）。そのあと `scriptIface` アダプタ一族を生成に切り替える。
6. `builtin/`。続いて、パッケージ単位の遅延 install を備えた `stdlib/`（§2.7）。
7. `loader/`（`runtime/package.go` と `minigo.go` からライフサイクルの状態機械を取り出す）。

このフェーズを終えると、root パッケージと `vm.go` はそれぞれ約 3,000 行を下回る。また `archtest` で、`vm/` での `reflect` の使用を禁止する（構築時に渡される `hostffi` アダプタの外では使わせない）。

### フェーズ 3 — 型モデル

1. intern 付きの `types.Type` を導入し、`TypeDef.Type()` 経由で `TypeDef` から構築する。最初は読むだけの利用側から移す: メソッドセット、インタフェース充足判定、`MethodSetOf`（1 回の走査にする）。
2. 同一性のチェックを `*types.Type` の `==` に移す。twin/shape 系のヒューリスティクスは 1 PR につき 1 つずつ消し、どの PR も pin 済みのリグレッションケースで裏付ける。
3. host 型: `reflect.Type` ごとに `*types.Type` を 1 つにし、bind された named 型と source 側の宣言を統合する。
4. ジェネリクスの推論とインスタンス化を `types/infer.go` に移す。インスタンスキャッシュのキーは、文字列で綴ったものではなく `(*Function, []*types.Type)` にする（「呼び出しごとのキー綴り」TODO が解消する）。
5. `types.Oracle` を導入し、`compile/` と `vm/` にある 14 箇所の `Materialize()` 呼び出しをこれ経由にする（C1〜C3）。`WithStrictLazyCompile` を追加し、このモードで conformance スイートを CI で回す。
6. `staticOpTyp`/`opTyp`/`OpLocalTyp`/`OpGlobalTyp`/`statics` を、oracle が裏付けるヒントに置き換える。ヒントは `CallSite` とスロットのメタデータに格納する。

### フェーズ 4 — VM の形

次の順に進める（前のステップが次を単純にする）。

1. bytecode verifier と `MaxStack`
2. スロット分類と直接ストア op
3. goroutine ごとの連続スタック
4. `CallSite` 記述子
5. `OpGlobal`/`OpSelect` のインラインキャッシュ

完了条件: 次の性能系 TODO がクローズされるか、意味を失っていること。
- 「frame allocation」
- 「non-param locals are allocated twice」
- 「operand stack grows from empty」
- 「`chunkCellsEscape` is true for nearly every chunk」
- 「selector resolution re-searches by name」

それを、コードを単純にした *副産物* として達成する（I6）。

### フェーズ 5 — 任意、判断ゲート付き

- 非常に大きなパッケージ向けに、シンボル単位で遅延 materialize する（plan-minigo-vm §10 の go-scan `FindSymbolInPackage` 経路）。realworld の実行で parse/index が支配的だと分かった場合に限ってやる。
- tagged な `Value` 表現は、フェーズ 4 のあとでも、プロファイルで boxing が単独で約 15% を超えるホットスポットだった場合に限って再検討する。デフォルトの答えは「やらない」。

## 5. リスクと緩和策

| リスク | 緩和策 |
|---|---|
| 大規模な移動が、進行中の機能やバグ修正の PR と衝突する | フェーズ 1 は短い期間に区切ってファイル単位で入れる。移動と編集を同じ PR に混ぜない |
| 型の intern で観測可能な同一性（`%T`、`reflect.Type.String`）が変わる | 表示は intern された `Type` からではなく、引き続き宣言（`TypeDef`）から導出する。difffuzz の `%T` プローブで固定する |
| oracle のヒントが意味論に影響してしまう（I5） | C3 と `WithStrictLazyCompile` の CI ジョブで防ぐ。ヒントにできるのは、高速化か、gc も行うチェックを厳しくすることだけ |
| `stdlib` の遅延 install で初期化順や副作用が変わる | bind は純粋な構築処理だけにする。2 つのエンジンと 1 つの `NewSession` が同一の bind パッケージを見ることを確かめるテストを追加する |
| 共有 chunk 上のインラインキャッシュの並行性 | サイトごとに atomic pointer を使う。`make test-race` と `make concurrency-stress` をフェーズ 4 のゲートにする |
| リポジトリ外の `VMCaller` 利用者 | `VMCaller` は union インタフェースとして残し、capability インタフェースのほうを推奨として文書化する |

## 6. 明示的な非目標

- 静的型検査器を導入すること、またはコンパイル時に型エラーのあるプログラムを拒否すること（I1/I2 に反する）。
- AST→AST マクロ（I4）。
- JIT、レジスタ VM への全面書き換え、tagged value への全面書き換え（I6）。
- スクリプトのソース規約や `minigo.dev/host` スタブ API の変更（I3）。
