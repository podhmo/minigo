# 計画: minigo のリアーキテクチャ（v1 以後の整理）

> **ステータス**: 提案。[spike-rearchitecture.md](./spike-rearchitecture.md) のスパイクを受けて改訂した。コードオーナーとしてパッケージ構成も VM の形も自由に変えてよい、という立場で書いた。ただしプロジェクトの根幹にある考え方は保つ。スナップショットは main `a3f4e24`（2026-11-17）。
>
> 英語版: [../plan-rearchitecture.md](../plan-rearchitecture.md)
> 関連: [../plan-minigo-vm.md](../plan-minigo-vm.md)（元の設計）、[todo-priority.md](./todo-priority.md)、[experiment-gosimilarity-refactoring.md](./experiment-gosimilarity-refactoring.md)、[../experiment-frame-path-perf.md](../experiment-frame-path-perf.md)

## この改訂での変更点

初稿の主張をスパイクで計測した。その結果を受けて変えた節には *（改訂）* を付けている。

- **§2.4。** 「コンパイル時の答えが unknown でも常に安全」（旧 C3）は誤りだった。コンパイル時の問い合わせの一部は、速度ではなく意味論を決めている。契約を *ヒント* と *事実* に分け（C3′）、`WithStrictLazyCompile` の意味を「事実をすべて遅延させる」に変えた。
- **§1.2.7、§2.5.1。** スロットが escape する主因はストアではなく、フィールドの読み出しと呼び出しレシーバだった。また `OpNewLocal`/`OpRenewVar` も毎回新しい cell を確保している。どちらもスロット分類の対象に含めた。
- **§2.2。** `VMCaller` には、インタフェースアサーションと `*VM` へのダウンキャストで使われている隠れた capability がある。それらを列挙した。
- **§2.3。** intern のキーを `reflect.Type` ではなく、表している型 (PkgPath, Name) にし、エンジン単位で intern することにした。`==` になるのは host twin 系の helper だけで、shape 系は残る。
- **§2.7。** 遅延 install の根拠を `NewEngine` ではなく `NewSession` のコストに置き直した。先に扱うべき結合が 4 つある。
- **§4。** フェーズ 0 に、計測でもっとも安く効くと分かった `pkg/locator` の `go env` のメモ化を加えた。

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
| `intrinsics.go` | 11,033 | `installStdlib` だけで約 3,000 行（単一関数）。45 の import パス、1,134 のメンバを bind している |
| `compile/compile.go` | 7,295 | スコープ、文、式、定数畳み込み、静的型ヒント、goto 検査、複合リテラル、特殊形式 |
| `runtime/value.go` + `typedef.go` + `package.go` | 5,662 | 値、`TypeDef`、パッケージのライフサイクル、`SpecialContext`、`VMCaller`、panic/trap。すべて一つのパッケージにある |
| `minireflect/` | 6,766 | スクリプト値用の reflect ファサード |
| root `minigo` | 約 18,000 | Engine、builtin、dispatch（メソッドセット）、inspect、iter.Seq、host vars、embed、intrinsic |

元のレイアウト（plan-minigo-vm §14 の `loader/`、`types/`、`builtin/`、`intrinsic/`、`special/`、`ffi/`、`debug/`）は実体化しなかった。それらの責務は `vm.go`、`runtime/`、root パッケージに溜まっている。

### 1.2 構造上の問題（コストの大きい順）

1. **型モデルが一つではない。** `runtime.TypeDef` は「遅延 AST 参照」（`Spec`、`Anon`、`EmbedSpecs`、`IEmbeds`、`TConstraints` が `ast.Expr`）と「解決済みの型」（`Elem`、`Embeds`、`Methods`、`Binds`、`HostIdent`、`HostNew`、`HostScalar`）を兼ねている。そのため型の同一性は、VM に散らばった多数のヒューリスティクスで近似されている。`tdShapeEq`、`sameSpelledTwin`、`hostNilTwin`、`hostTwinIs`、`peelAlias`、`structFieldsEq`、`underlyingShape`、`typeExprFor`、`reflectTypeExpr` などがそれにあたる。gosimilarity の調査では、「`td.Anon` が nil なら `td.Spec.Type`」という慣用句が 18 箇所に、TypeDef の構築が 4 箇所に重複していた。TODO.md の正しさに関するバグの多く（named と host の twin、alias の同一性、host embed の promote フィールド）がこの継ぎ目にある。
   *（改訂）* twin がどこから生じるか: bind されたパッケージが返す host 値の型が、source 解釈されているパッケージで宣言されている、という状況で生じる。たとえば、`io/fs` が source 解釈のときに `os` が `*fs.PathError` を返す場合や、host の `go/ast` ノードが source 解釈の `go/ast` と出会う場合である。実行時の合成がこれを悪化させている。`vm.hostTypedefOf` と `hostPkgElemTypedef` は呼び出しのたびに新しい TypeDef を作り、1 回のスイート実行で `ast.Expr` のコピーが 163 個できていた。
2. **静的型の知識がコンパイラと VM に分かれている。** コンパイラは場当たり的に部分的な静的型を求め（`staticOpTyp`、`callOpTyp`、`stampVarTyps`、`opTyp`）、それを `OpLocalTyp`/`OpGlobalTyp` のスタンプと呼び出しごとの `statics []*TypeDef`（vm.go で 50 箇所）として VM に渡している。VM はそれを推論と型なし定数のデフォルト化に使う。コンパイラが「何を知ってよいか」という契約には名前すら付いていない。
3. **コンパイル時の遅延性の契約が暗黙のままになっている** *（改訂）*。
   - コンパイラは `pkg.Name` のようなオペランドの型を求めるために、`compile/` の 8 箇所で `ImportRef.Materialize()` を呼んでいる。`DisplayName` 経由で `runtime/typedef.go` の呼び出しにも到達する。
   - 計測すると、コンパイルがパッケージを initialize したことは一度もなかった。root スイートでのパッケージのビルドのうち 6.5%（2,502 回中 162 回）はコンパイラから始まっている。
   - 「コンパイルは *Indexed* まで進めてよい」と書かれていないこと以上に問題なのは、これらの答えの一部が *意味論的* だということだ。`pkg.C` が定数かどうか、`pkg.T` が nil になり得るか、`rc == NoBody` を interface 比較に下ろすための静的型、複合リテラルのキーの型がそうである。
   - コンパイラがこれらに答えないと、スイートで誤った出力が 4 件、偽の trap が 1 件、gc が拒否するはずのプログラムが通るものが 9 件出る。どの答えがどちらの種類なのかは、どこにも示されていない。
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
5. **`VMCaller` が 21 メソッドのインタフェースになっている。** builtin と intrinsic はすべてこれに依存している。そのため、どれか一つを root パッケージや `vm` の外へ出そうとすると、`VMCaller` ごと引きずることになる。*（改訂）*
   - これを受け取る 360 関数のうち、96 は一切使わず、184 は helper に受け渡すだけだった。
   - 実際の表面は 21 メソッドより広い。呼び出し側は無名インタフェースへの型アサーションで `HostClear`、`HostMapDelete`、`HostNilTwin`、`MaterializeConstErr`、`CallCatching`、`ElemZero` を使っており、`vm.vmOf` は 7 箇所で `*VM` にダウンキャストしている。
6. **intrinsic テーブルがパッケージ単位の bind に戻っている。** これは plan-minigo-vm §11 がなくそうとしていたものそのものである。実務上は正しい判断だった（experiment-oapi-codegen-perf によれば「~7% を超えて効くレバーは native 実行だけ」）。しかし今の形は 3,000 行の単一関数で、パッケージごとの所有者もテストもない。`minigo gen-intrinsics` があるのに、生成されたファイルも一つもない。*（改訂）*
   - このテーブルはキャッシュされず、エンジン *とセッション* ごとに作り直されている。
   - `NewSession` 経由の最小の実行では、実行時間の 60〜70%、アロケーションの約 94% を占める。
   - `NewEngine` 経由では実行時間の約 7% にとどまる。そちらでは `pkg/locator` の 2 つの `go env` サブプロセスが支配的である。
7. **frame とローカル変数のレイアウトが構造上悲観的になっている** *（改訂）*。
   - ローカル変数はすべてヒープ上の `*Cell` になる。
   - `OpLocalRef` はストア先だけでなく、変数に対する selector の読み出しと呼び出しレシーバにも出る（`selectorBaseIsVar`）。読み出しとレシーバが `OpLocalRef` 全体の約 70% を占める。その結果、`chunkCellsEscape` が true になる chunk は 44.5% で、正確な規則なら約 10% で済む。
   - escape しない chunk でも節約できる量は小さい。`OpNewLocal` は `:=`/`var` ごとに、`OpRenewVar` はループの反復ごとに新しい cell を確保するので、cell ブロックの再利用（#1024）が効くのは引数だけである。
   - operand stack も frame ごとに伸びていく。
   - これは *設計* の問題であり（コンパイラがスロットを分類していない）、性能系の TODO はその症状にすぎない。

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
                            types.Oracle 経由のヒントと事実)
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

### 2.2 `VMCaller` を capability インタフェースに分割する *（改訂）*

メソッドを小さなインタフェースに分ける。VM がすべてを実装し、利用側は必要最小限のものだけを受け取る。分割には現在の隠れた capability も含める必要がある。そうしないと、bind を外へ出しても具象の `*VM` を引きずることになる。

```go
package runtime

type Caller interface   { Call(fn Value, args []Value) (Value, error); CallCatching(fn Value, args []Value) (Value, error); Recover() Value }
type Spawner interface  { Spawn(fn Value, args []Value) *Task; Task() *Task }
type TypeOps interface  { TypeOf(Value) *TypeDef; Zero(*TypeDef) Value; ElemZero(*TypeDef) Value; Convert(*TypeDef, Value) (Value, error); Equal(*TypeDef, Value, Value) bool; Copy(Value) Value; NewMap(*TypeDef) *Map; MapKeyOperand(*TypeDef, Value) Value }
type TypeResolver interface { ResolveType(from *TypeDef, x ast.Expr) (*TypeDef, error); FieldTypes(*TypeDef) ([]*TypeDef, error); ElemOf(*TypeDef) (*TypeDef, error); ArrayLenOf(*TypeDef) (int64, bool) }
type Members interface  { Member(base Value, name string) (Value, bool); MethodSetOf(Value) (map[string]bool, bool) }
type HostOps interface  { HostClear(Value) bool; HostMapDelete(m, k Value) bool; HostNilTwin(*TypeDef) Value; MaterializeConstErr(Value) error } // 現状は型アサーション経由
type Frames interface   { CallerPCs() []uintptr; CallerFrame(pc uintptr) (CallSite, bool); Package() *Package }

// 移行中の source 互換のため、VMCaller はこれらの union として残す。
type VMCaller interface { Caller; Spawner; TypeOps; TypeResolver; Members; HostOps; Frames }
```

（上の `HostOps` のシグネチャは仮のもので、実際のシグネチャは既存のアサーション箇所から取る。）

スパイクで計測した利用状況: `Call` が 24 関数、`TypeOf` 16、`Convert` 13、`ElemZero` 12、`Zero` 10、`ElemOf` 6、その他はすべて 5 以下。大半の bind は何も要らないか、`Caller` だけで足りる。これが bind を root パッケージから外へ出す前提条件になる。

移行の順序:

1. 受け渡し先の helper（`runtime.IfaceMember`、`hostErrOf`、`display`、`callOrPanic`、`goJSONSeen` など）を先に capability 型にする。caller を受け渡すだけの 184 関数が本当に何を必要とするかは、これらの helper で決まる。
2. caller を一切使わない 96 関数から引数を削る。
3. `vmOf` によるダウンキャスト 7 箇所を capability のメソッドに置き換える。

### 2.3 型モデルを一つにする: `types.Type` + `types.Ref` *（改訂）*

```go
package types

// Ref はレキシカルな文脈付きの、未解決で遅延な型式。
// インデックス作成時に生成され、作るために何もロードしない。
type Ref struct { Expr ast.Expr; Scope *Scope }   // Scope: ファイルの import + ローカル型 + ジェネリクス束縛

// Type は解決済みの型。Universe（Engine ごとに 1 つ）単位で intern される。
type Type struct {
    Kind    Kind
    Named   *NamedInfo   // パッケージパス + 名前 + 型引数（無名型なら nil）
    Elem    *Type; Key *Type; Len int64
    Fields  []Field      // 遅延かつ一度だけ解決（埋め込みパスも事前計算）
    Methods *MethodSet   // 遅延かつ一度だけ構築。アサーションと dispatch で共有
    Host    reflect.Type // host 側の運搬型（ボックス型のこともある）
    Def     *runtime.TypeDef // 移行中の逆参照
}

type Oracle interface {
    // Resolve はパッケージを Indexed まで進めてよいが、Initializing には進めない。
    Resolve(Ref) (*Type, error)
}
```

- **intern のキー。** named 型は、`reflect.Type` ではなく *表している* 型 (PkgPath, Name, 型引数) をキーに intern する。
  - 運搬型がボックスのことがある。`atomicBox` は `atomic.Value` と `atomic.Pointer` の両方を裏で支えており、`timerChanBox`/`afterFuncTimer` は `*time.Timer` を表している。`reflect.Type` をキーにすると、こうした別々の型を誤って統合してしまう。
  - bind された named 型（`time.Duration`）とその source 側の宣言は、同じ `*Type` に解決させる。
- **Universe = Engine。** 現状 bind テーブルはエンジンごとに作り直されている（99 個の host 型がエンジンごとに新しい TypeDef を得ていた）。プロセス全体で同一性を持たせるには、まず §2.7 のテーブル共有が必要になるが、それは計画に含めない。
- **host typedef の経路を 1 本にする。** host typedef はすべて intern テーブルを通す。対象は `hostTypedefOf`、`hostPkgElemTypedef`、`HostTypeOf`、ジェネリクスのインスタンス化によるコピーである。
- **`==` になるもの:** `hostTwinIs`、`hostNilTwin`、`sameSpelledTwin`、`HostNamedIs`/`HostTwinOf`、`sourceTwinOf`。
- **残るもの:**
  - `tdShapeEq`、`underlyingShape`/`convShapeEq`、`structFieldsEq` は、*異なる型同士* について Go の「underlying が同一」の規則と代入可能性の規則を実装している。intern してもなくならない。無名の複合型を hash-consing すれば安くはなるだけである。
  - `peelAlias` は、alias を遅延解決する限り残る。
- **値の表現は 2 種類のまま。** 型を統合しても、値は `*runtime.Struct` としても `GoValue` としても持たれ得る。変換層（`hostErrTwins`、`goNative`、プロキシのボックス）は残し、`hostffi/` に移す。
- `MethodSet` は型ごとに一度だけ計算し、`MethodSetOf`、`MethodFuncsOf`、インタフェース充足判定、promote されたメンバの検索のすべてで使う。これで「メソッドセットを二度歩く」TODO が構造的に解消する。
- selector 解決では、`(Type, name)` ごとに *フィールドインデックスのパス* をキャッシュする。これで「selector 解決が名前で探し直す」TODO が解消する。
- `runtime.TypeDef` は宣言レコード（index と compile が作るもの）として残し、`TypeDef.Type()` が intern 済みの `*Type` を遅延で返すようにする。移行は呼び出し箇所ごとに進め、一斉切り替えはしない。

### 2.4 コンパイル時の遅延性の契約を明文化する *（改訂）*

> **C1.** 関数をコンパイルするとき、その関数内で *構文上* 参照されているパッケージは `Indexed` まで進めてよい。`Initialize` は決して実行しない。（計測では、現状でもスイートと examples 全体で違反 0 件で成り立っている。）
>
> **C2.** そうしたパッケージの読み込みは、コンパイラが所有する単一の `types.Oracle` を必ず経由する。`compile/` から `ImportRef.Materialize()` を直接呼んではならない。
>
> **C3′.** コンパイル時の問い合わせは、すべて次のどちらかに分類する。
>
> - **ヒント** は速くするだけのものである。例: 動的な検索を省くための静的な引数 typedef や、インラインキャッシュの初期値。oracle が答えられなければヒントは捨て、VM が動的に判断する。
> - **事実** は意味論を決める。例: `pkg.C` は定数か（シフト回数の定数性）、`pkg.T` は nil になり得るか、`==` の下ろし方を決めるオペランドの静的型は何か、複合リテラルのキーの型は何か。事実は決して捨てない。コンパイラが答える（C1 の範囲で許される）か、*遅延された事実* を出力する。遅延された事実は `CallSite` に `types.Ref` を持たせたオペランドで、VM が最初の実行時に一度だけ解決する。
>
> gc の静的な拒否（`mismatched types time.Duration and int64`）も事実に含まれる。遅延された拒否は、その地点の `TRAP` になる（I2 と整合する）。

初稿の C3（「unknown は常に安全」）を置き換えた理由: 一切答えない oracle で回すと、スイートで誤った出力が 4 件、偽の trap が 1 件、gc の拒否が失われるものが 9 件出た。すでに `Indexed` のパッケージにだけ答える oracle でも 13 件が失敗し、どれが失敗するかはロード順に依存した（[spike-rearchitecture.md](./spike-rearchitecture.md) の S2 を参照）。

これで `compile/typehint.go` が `staticOpTyp`、`callOpTyp`、`opTyp`、`stampVarTyps` を置き換える。その出力は chunk に格納される命令ごとの静的情報（2.5 参照）で、ヒントと、事実または遅延された事実からなる。`OpLocalTyp` のプッシュや呼び出しごとの `statics` スライスといった裏ルートは使わない。

`WithStrictLazyCompile()` は、oracle に *すべての* 事実を遅延させ、すべてのヒントを捨てさせるモードとする。このモードで conformance スイートを走らせても結果が同一で、違うのは遅くなることだけでなければならない。これは意味のある同値性チェックになる。すべての事実に実行時の経路があることを証明できるからである。初稿の定義（「`Indexed` でないものには答えない」）では、結果がロード順に依存するので同値性チェックにならなかった。

### 2.5 VM の形

以下はそれぞれ独立して入れられ、入れるたびに計測する。

1. **コンパイル時にスロットを分類する** *（改訂）*。
   - 各ローカル変数を次のどちらかに分ける。
     - `Plain`: frame のスロット配列に値を直接置く。
     - `Boxed`: `*Cell` にする。Boxed になるのは、親の upval を経由しないクロージャにキャプチャされる場合、`&x`/`&x.f`/`&x[i]` でアドレスを取られる場合、ポインタレシーバのメソッドに束縛される場合、特殊形式の呼び出し地点から見える場合である。特殊形式については、`trySpecial` が見えるローカルをすべて `QuotedCall.Locals` にスナップショットし、`specialCtx.Eval` が呼び出し元の cell を共有するためである。
   - ストアだけでなく、次の 3 種類の操作を変える必要がある。
     - ストア先には、`OpLocalRef` と汎用 ref ストアの組み合わせではなく直接の op（`OpSetLocal`、`OpSetLocalIndex`、`OpSetLocalField`）を使う。
     - Plain なローカルに対する selector の読み出しは `OpLocal` + `OpSelect` にする。
     - 呼び出しのレシーバ `x.M()` では、`M` のレシーバの種類を知る必要がある。これは §2.4 の意味での *事実* であり、静的に答えるか、分からなければ保守的に Boxed にする。
   - Plain なローカルでは、`OpNewLocal` と `OpRenewVar` がスロットにその場で書き込むようにする。そうしないと、escape しない chunk でも `:=` とループの反復ごとに cell を確保し続ける。
   - 期待される効果（スパイク S3/S4）: escape する chunk は約 45% から約 10% に減り、Boxed のまま残るローカルは約 5% になる。
   - これで非引数ローカルの二重アロケーションがなくなり、`chunkCellsEscape` が正確になり、`assignRef` が *単純になる*。
   - frame の cell を汎用的に読んでいて、移植が必要な箇所:
     - `selectMember`/`structMember` の `case *Cell`
     - `assignRef`
     - `specialCtx.Resolve`
     - `localTypedefOf`
     - 名前付き戻り値
     - range のイテレータのスロット

     いずれもブロッカーではない。
2. **goroutine ごとに連続した値スタックを 1 本持つ。** frame はその中の窓 `(base, sp)` とし、`Chunk.MaxStack` は bytecode verifier が求める。引数はその場で渡す（`popArgs` のスライスも、bound method でレシーバを先頭に挿入するためのコピーも要らなくなる）。
3. **chunk に `CallSite` 記述子を持たせる。** 次のものを 1 つの不変な `bytecode.CallSite` にまとめ、`OpCall` のオペランドから参照する。
   - 静的な引数 typedef（ヒント）
   - spread 要素の typedef
   - 特殊形式の quote インデックス
   - 遅延された事実（§2.4）
   - ソース位置

   `frame.argbuf/statbuf` と `deferredCall.statics/spreadTd` は不要になる。
4. **`CallSite`/`Site` のスロットにインラインキャッシュを置く。** atomic pointer にするので、共有 chunk でも race-free のままになる。`OpGlobal` は解決済みの cell をキャッシュし、`:pin` や再 bind で進む `Env` epoch でガードする。`OpSelect` は `(*Type → アクセスパス)` をキャッシュする。これは plan-minigo-vm §16 にある「後で入れる `OpSelect` インラインキャッシュ」にあたり、型が intern されたことで初めて可能になる。解決済みの遅延された事実も同じスロットの仕組みを使う。
5. **bytecode verifier**（`bytecode.Verify`）を、テスト時とデバッグオプション指定時に走らせる。スタック効果の整合性、ジャンプ先、スロット種別を検査する。これでコンパイラのバグの一部がその場でテスト失敗として現れるようになり、`MaxStack` も得られる。
6. **維持するもの**: boxed コンポジットを使う `Value = any`、多値返却の `Tuple`、`Trap` と `Panic` の区別、関数単位の遅延コンパイル、`SPECIAL_CALL`。tagged な `Value` 構造体は明示的に **スコープ外** とする（I6。プロファイルはフラットで、効果が実証されたレバーは native な bind である）。
7. **`OpEvalAST` を廃止する。** ただし REPL/`EvalExpr` のブリッジとしての用途は残す。v1 からの移行という役目は終わったので、`OpEvalFragment` に改名し、ブリッジ用の op として文書化する。（呼び出し元のローカルを使わないので、スロット分類の影響は受けない。）

### 2.6 host 境界をパッケージにする: `hostffi/`

`vm.go` から次のものを移す（現状のおおよそ 3,950〜8,600 行目と 12,000〜14,400 行目）。

- `goValueOf`、`toReflectValueDepth`、`hostMarshal`、`adaptFunc`、`hostArgAs`
- opaque store、host の nil/twin ヘルパ
- `scriptIface` 一族（`io.Reader`、`io.Writer`、`io.Seeker`、`fs.FS`、`fs.File`、`fs.FileInfo`、`fs.DirEntry`、`sort.Interface`、`fmt.Stringer` など）
- §2.3 で残すと決めた host↔script の値の変換層（`hostErrTwins`、`goNative`、プロキシのボックス）

アダプタ一族は、組み合わせごとの手書き（`scriptFileSeek`、`scriptFileSeekWT`、`scriptDirFileSeekWT` などの組み合わせ爆発）をやめ、テーブル（インタフェース → メソッド → arity）から生成する。

### 2.7 bind パッケージを `stdlib/<path>` にする *（改訂）*

```text
stdlib/
  registry.go          names: import path → メンバ名（安価なので即時に持つ）
                       tables: import path → 遅延構築される *runtime.Package
  bind/                ヘルパ（fn, fnvc, fnvcRaw, hostType, …）— 今の hostHelpers
  strings/  bytes/  strconv/  fmt/  time/  os/  encoding/json/  text/template/ …
```

- 各サブパッケージは `func Install(r *bind.Registry, env bind.Env)` を公開する。パッケージのテーブルは初めて必要になったときに *構築* する。`installStdlib` の中にパッケージをまたぐ静的な結合はほとんどないので、分割の大半は機械的な作業になる。
- **根拠。** 得をするのはセッションである。`NewSession` は install 全体をやり直していて、それが最小のセッション実行の実行時間の 60〜70%、アロケーションの約 94% を占める。`NewEngine` については、`pkg/locator` の `go env` サブプロセスのほうがコストが大きく、直すのも安い（§4 フェーズ 0）。
- **遅延にする前に扱うべき結合:**
  1. *host 型の検索で install を起動する。* `hostTwin` は、*他の* パッケージから流れてきた host 値（`ctx.Deadline()` が返す `time.Time` など）について、`e.pkgs[rt.PkgPath()]` から bind 済みの typedef を探している。この検索で install を起動しないと、黙って nil が返る（I5 上の危険）。
  2. *名前のレジストリをテーブルと分ける。* `BoundPackages`、`BoundSymbols`、`BoundOf`、`vet`、`SourceOf`、`NewSession` の bind の優先順位、`loadPathMode` の bound-shadow チェックは、いずれも bind 済みの集合を即時に読んでいる。これらは安価な `names` ビューに移す。
  3. *エンジンの状態を明示的な `bind.Env` にする。* 約 45 個のクロージャがエンジンをキャプチャしている。対象は `fsPath`、`cwd`（`os.Chdir` が書き込む）、`args`、出力先、そして `os`/`host` のテーブルの中身を決める `AllowedRoots` である。これを明示的に渡す形にしても、テーブルはエンジンごとのままである。エンジンやセッションをまたいで共有するには、この状態を呼び出しコンテキストへ移す必要があるが、それはこの計画の目標ではない。
  4. *グローバルで可変な typedef をなくす。* `templateFuncMap` はパッケージレベルの TypeDef 1 つを全エンジンで共有しており、`Bind` はその `Pkg` を一度しか設定しない。これをレジストリごとに持つ。
- **生成。**
  - 869 エントリの約半分は、現在の `minigo gen-intrinsics` で生成できる形（1 文のラッパ、`GoValue` の値、定数）をしている。これらは `// Code generated` ヘッダ付きでチェックインする。
  - メソッドリストや `HostScalar` を持つ typedef（約 12%）は、先に生成器を拡張する必要がある。
  - 約 25% は本物のアダプタ（コールバック、チャネル、タイマー、`FuncMap`、`sync.*` のボックス、ジェネリクス）で、手書きのまま残す。
  - 生成したラッパは `WrapFunc` を経由し、呼び出しのたびに reflect を使う。そのため現在の直接ラッパより遅い。I6 の下ではこれを受け入れるが、ホットなパッケージは手書きのラッパを残してよい。
- `--src` が通るものについては、bind した各パッケージに、source 解釈版との parity テストを付ける（difffuzz の判定契約を使う）。bind がずれたときに黙って誤答するのを防ぐガードになる（I5）。
- `WithAllowedRoots`/`WithHostPolicy` は、個々の bind の中でチェックするのをやめ、レジストリへのフィルタにする（`hostPolicy` はすでに `Bind` の中で汎用的にフィルタしている）。

### 2.8 特殊形式を `special/` にする

`runtime.SymbolID`、`QuotedCall`、`SpecialFunc`、`SpecialContext` と `vm.specialCtx` を `special/` に移す。`SpecialContext.ResolveType` は `types.Ref`/`*types.Type` を返すようにする。これで convert-define に、plan-minigo-vm §12.5 が描いた API を実際に提供できる。登録は引き続き `Engine.RegisterSpecial` で行う（I7）。コンパイラが参照するのはレジストリの `Has(SymbolID)` だけにする（I4: スタブパッケージは決してロードしない）。同じチェックで、見えるローカルを Boxed にする（§2.5.1）。

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

現状、`vm` は `minireflect` を import しており、`runtime` はすでに `bytecode` を import している。どちらも、`minireflect` を `hostffi` の下に移せば目標の形に収まる。

## 4. 移行計画

各フェーズは振る舞いを保存する。それぞれを小さな PR の列として入れ、`make format lint test`、`make test-race`、pin 済みの difffuzz ケース、examples をすべて通す。フェーズ 1〜2 は機械的な作業で、3〜4 で設計変更を行う。

### フェーズ 0 — ガードレール（小さく、最初にやる）

- *（改訂）* `pkg/locator` で `go env GOROOT`/`GOMODCACHE` をプロセス単位でメモ化する。他のどの作業とも独立しており、`NewEngine` の単独で最大のコストである。
- `archtest_test.go`: 各パッケージの import を `parser.ParseFile(…, parser.ImportsOnly)` で読み（`go list` は使わない）、§3 の表を満たすことを確かめる。最初は現在のグラフを許可集合とし、フェーズごとに締めていく（ラチェット）。
- ファイルサイズのラチェット: 生成ファイルでもテストでもない `.go` ファイルが記録済みのサイズを超えたら失敗するテスト（予算は縮む一方）。
- ベースラインを記録する: `session_bench_test`、`genericbench_test`、realworld の `grafana-openapi` の実行時間とアロケーションバイト数、それにスパイクで使った escape とコンパイル起因ロードのカウンタ。以降のフェーズではすべて差分を報告し、10% を超える劣化には PR で理由を書く。
- `bytecode.Disasm` の golden テストセット（代表的な関数を十数個）を追加する。コンパイラのリファクタリングの影響がテキストの diff として見えるようになる。

### フェーズ 1 — ファイル分割のみ、パッケージは動かさない（機械的）

- `vm/vm.go` を、既存の `// ----` セクションコメントに沿って次のファイルに分ける: `loop.go`、`frame.go`、`call.go`、`panic.go`、`trace.go`、`sched.go`、`ffi_marshal.go`、`ffi_iface.go`、`const.go`、`binop.go`、`convert.go`、`assert.go`、`generic.go`、`special.go`、`select.go`、`composite.go`、`iter.go`。
- `compile/compile.go` を次のファイルに分ける: `scope.go`、`stmt.go`、`assign.go`、`expr.go`、`call.go`、`lit.go`、`constfold.go`、`typehint.go`、`goto.go`、`special.go`。
- `intrinsics.go` を bind する import path ごとに 1 ファイルへ分ける。`installStdlib` は `installStrings(h)`、`installTime(h)` … の呼び出しを並べるだけの関数にする。
- `git diff --color-moved=dimmed-zebra` でレビューする。ロジックの変更はゼロ。
- scratch worktree で検証済みの gosimilarity R1〜R6 の統合（正味 −452 行）もここで取り込む。`(*TypeDef).TypeExpr()` もこれに含まれる。

### フェーズ 2 — 既存の継ぎ目に沿ってパッケージを抽出する

各ステップのインタフェースが狭くなるように、次の順で進める。

1. capability インタフェース（§2.2）。`VMCaller` はその union にする。受け渡し先の helper から始め、次に隠れたアサーションを取り込み、最後に `vmOf` のダウンキャストをなくす。
2. `vm/sched`（goroutine、park/wake、デッドロック検出）。ループとは `spawn`/`park`/`unpark` を通してしかやり取りしない。
3. `special/`（§2.8）。
4. `constant/`（UConst の演算とデフォルト化）。
5. `hostffi/`（§2.6）。そのあと `scriptIface` アダプタ一族を生成に切り替える。
6. `builtin/`。続いて `stdlib/`（§2.7）を次の 3 段階で進める。
   1. 名前のレジストリと `bind.Env` を導入する（テーブルはまだ即時に構築する）。
   2. `hostTwin` から install を起動するようにする。
   3. テーブルを遅延構築にする。
7. `loader/`（`runtime/package.go` と `minigo.go` からライフサイクルの状態機械を取り出す）。

このフェーズを終えると、root パッケージと `vm.go` はそれぞれ約 3,000 行を下回る。また `archtest` で、`vm/` での `reflect` の使用を禁止する（構築時に渡される `hostffi` アダプタの外では使わせない）。

### フェーズ 3 — 型モデル

1. エンジン単位で intern する `types.Type` を導入し、`TypeDef.Type()` 経由で `TypeDef` から構築する。最初は読むだけの利用側から移す: メソッドセット、インタフェース充足判定、`MethodSetOf`（1 回の走査にする）。
2. *（改訂）* host typedef を作る箇所（`hostTypedefOf`、`hostPkgElemTypedef`、`HostTypeOf`、インスタンス化によるコピー）をすべて、表している (PkgPath, Name) をキーにした intern テーブル経由にする。プロキシのボックスは、表している型に対応づける。
3. 同一性のチェックを `*types.Type` の `==` に移す。host twin 系の helper 5 つを 1 PR につき 1 つずつ消し、どの PR も pin 済みのリグレッションケースで裏付ける。shape 系の helper（`tdShapeEq`、`underlyingShape`/`convShapeEq`、`structFieldsEq`）と `peelAlias` は残し、Go の同一性規則と代入可能性規則として `types/` に移す。
4. ジェネリクスの推論とインスタンス化を `types/infer.go` に移す。インスタンスキャッシュのキーは、文字列で綴ったものではなく `(*Function, []*types.Type)` にする（「呼び出しごとのキー綴り」TODO が解消する）。
5. *（改訂）* `types.Oracle` を導入する。まず `compile/` の 8 箇所の呼び出しと、コンパイラが到達する `runtime/typedef.go` の呼び出しを、*ヒント* か *事実* に仕分ける。次にそれらを oracle 経由にし（C1、C2）、すべての事実に遅延経路を用意する（C3′）。そのうえで初めて `WithStrictLazyCompile`（事実をすべて遅延させる）を追加し、このモードで conformance スイートを CI で回す。受け入れ条件は、答えない oracle で現在失敗している 13〜15 件のテストである: `gate_*`、`shift_const_count`、`litkey_named_peel`、`assign_pkgname_differs`、`src_pkgtype_maplit_keys`、`selnilable_trap`、`selconv_typearg`、`TestNetHTTPRoundtrip`。
6. `staticOpTyp`/`opTyp`/`OpLocalTyp`/`OpGlobalTyp`/`statics` を、oracle が裏付けるヒントと事実に置き換える。これらは `CallSite` とスロットのメタデータに格納する。

### フェーズ 4 — VM の形

次の順に進める（前のステップが次を単純にする）。

1. bytecode verifier と `MaxStack`
2. スロット分類（ストア、selector の読み出し、レシーバを対象とし、`OpNewLocal`/`OpRenewVar` はその場に書き込む）
3. goroutine ごとの連続スタック
4. `CallSite` 記述子
5. `OpGlobal`/`OpSelect` のインラインキャッシュ

完了条件: 次の性能系 TODO がクローズされるか、意味を失っていること。
- 「frame allocation」
- 「non-param locals are allocated twice」
- 「operand stack grows from empty」
- 「`chunkCellsEscape` is true for nearly every chunk」（目標: escape する chunk が約 10%）
- 「selector resolution re-searches by name」

それを、コードを単純にした *副産物* として達成する（I6）。

### フェーズ 5 — 任意、判断ゲート付き

- 非常に大きなパッケージ向けに、シンボル単位で遅延 materialize する（plan-minigo-vm §10 の go-scan `FindSymbolInPackage` 経路）。realworld の実行で parse/index が支配的だと分かった場合に限ってやる。
- エンジン捕捉の状態を呼び出しコンテキストへ移し、bind テーブルをエンジンやセッションをまたいで共有する。遅延 install のあとでもセッションの起動コストが大きい場合に限ってやる。
- tagged な `Value` 表現は、フェーズ 4 のあとでも、プロファイルで boxing が単独で約 15% を超えるホットスポットだった場合に限って再検討する。デフォルトの答えは「やらない」。

## 5. リスクと緩和策

| リスク | 緩和策 |
|---|---|
| 大規模な移動が、進行中の機能やバグ修正の PR と衝突する | フェーズ 1 は短い期間に区切ってファイル単位で入れる。移動と編集を同じ PR に混ぜない |
| 型の intern で観測可能な同一性（`%T`、`reflect.Type.String`）が変わる | 表示は intern された `Type` からではなく、引き続き宣言（`TypeDef`）から導出する。difffuzz の `%T` プローブで固定する |
| host の運搬型を共有する別々の型を intern で統合してしまう（`atomicBox`） | キーは表している (PkgPath, Name) にし、`reflect.Type` は決してキーにしない。`atomic.Value` と `atomic.Pointer` の pin 済みケースを追加する |
| コンパイル時の答えが意味論的だと判明する（I5） | C3′: すべての問い合わせをヒントか事実に分け、事実は答えるか遅延させ、決して捨てない。`WithStrictLazyCompile` の CI ジョブで、すべての事実に実行時の経路があることを確かめる |
| 遅延された事実によってエラーの出るタイミングが変わる（gc はコンパイル時に拒否し、minigo は実行時に trap する） | I2 の下で許容する。メッセージの文言は既存の `gate_*` ケースで固定する |
| 遅延 `stdlib` install で、他のパッケージから来た host 値の bind 済み typedef を見落とす | `hostTwin` の検索で install を起動する（§2.7 の結合 1）。`time` を import しないまま `ctx.Deadline()` → `time.Time` を流すテストを追加する |
| `stdlib` の遅延 install で初期化順や副作用が変わる | bind は純粋な構築処理だけにする。2 つのエンジンと 1 つの `NewSession` が同一の bind パッケージを見ることを確かめるテストを追加する |
| 共有 chunk 上のインラインキャッシュの並行性 | サイトごとに atomic pointer を使う。`make test-race` と `make concurrency-stress` をフェーズ 4 のゲートにする |
| リポジトリ外の `VMCaller` 利用者 | `VMCaller` は union インタフェースとして残し、capability インタフェースのほうを推奨として文書化する |

## 6. 明示的な非目標

- 静的型検査器を導入すること、またはコンパイル時に型エラーのあるプログラムを拒否すること（I1/I2 に反する）。遅延された事実は実行時に trap するのであって、プログラムを拒否するのではない。
- AST→AST マクロ（I4）。
- JIT、レジスタ VM への全面書き換え、tagged value への全面書き換え（I6）。
- スクリプトのソース規約や `minigo.dev/host` スタブ API の変更（I3）。
- プロセス全体での型の同一性や、エンジン間での bind テーブルの共有（フェーズ 5 を参照）。
