# 計画: minigo のリアーキテクチャ（v1 以後の整理）

> **ステータス**: 提案（第 3 版）。次の 3 つを統合した。
>
> - **D-own**: 所有権とライフタイムの設計（[discussion #1090 のコメント 19667469](https://github.com/podhmo/minigo/discussions/1090#discussioncomment-19667469)）。
> - **D-vm**: この計画の初稿。VM の形、capability インタフェース、パッケージ単位の stdlib を扱っていた。
> - **スパイク**: D-vm の前提を計測した [spike-rearchitecture.md](./spike-rearchitecture.md)。
>
> 両者が衝突する箇所は、どちらかを既定とはせず、筋の良さを比較して選んだ（§8）。コードオーナーとしてパッケージ構成も VM の形も自由に変えてよい、という立場で書いた。ただしプロジェクトの根幹にある考え方は保つ。スナップショットは main `a3f4e24`（2026-11-17）。
>
> 英語版: [../plan-rearchitecture.md](../plan-rearchitecture.md)
> 実行用のタスク一覧: [../../next/ja/TODO.md](../../next/ja/TODO.md)
> 関連: [../plan-minigo-vm.md](../plan-minigo-vm.md)（元の設計）、[todo-priority.md](./todo-priority.md)、[experiment-gosimilarity-refactoring.md](./experiment-gosimilarity-refactoring.md)、[../experiment-frame-path-perf.md](../experiment-frame-path-perf.md)

## 3 つの情報源の関係

2 つの設計は答えている問いが違うので、大部分はそのまま組み合わせられる。

- **D-own** は「誰が何をいつまで所有するか」を決める。
  - ソース由来の不変な定義は共有し、実行状態は Session ごとに持つ。
  - 何も実行せずに宣言と型の問いに答える semantics サービスを置く。
  - 「まだ分からない」を明示する結果の種類を定める。
- **D-vm** は、その枠の中で「実行がどう動くか」を決める。
  - frame とスロットの形
  - `CallSite` 記述子
  - `VMCaller` を置き換える capability インタフェース
  - パッケージ単位の遅延 bind

衝突する箇所は §8 で筋の良さを比較し、選んだ理由を記録した。下の表はその結論だけをまとめたもの。

| 論点 | D-vm（初稿） | D-own | スパイクの証拠 | 採用（§8 参照） |
|---|---|---|---|---|
| コンパイル時の問い合わせ | unknown は常に安全（C3） | 結果は Found / Missing / **Deferred** / Failed。Deferred は不正な Go ではない | unknown は安全では *ない*。誤出力 4、偽の trap 1、gc の拒否の喪失 9（S2） | D-own の結果の種類に、スパイクのヒント/事実の区別を加える（§2.4） |
| strict モード | すでに `Indexed` のものにだけ答える | ポリシー `IndexedOnly` | `IndexedOnly` 単独では 13 件が失敗し、ロード順に依存する | すべての事実に Deferred の経路があって初めて `IndexedOnly` は正しい（§2.4） |
| 型の同一性 | `reflect.Type` ごとに intern した `*Type`、ポインタの `==` | 宣言元ソース、位置、型引数、ソース版からなる `TypeID`。TypeDef のポインタは Session ごとに異なってよい | `reflect.Type` をキーにするのは誤り（`atomicBox`）。`==` になる helper は 9 中 5 | `semantics.TypeID` をキーにし、実行時の intern は Session ごとに `TypeID` で引く（§2.3） |
| semantics の依存 | `types` が `runtime` を import する（`Def` の逆参照あり） | `semantics` は runtime、VM、compiler に依存しない | — | D-own。アダプタは実行側に置く（§2.3） |
| レイアウト | `types/`、`loader/`、`hostffi/`、`stdlib/`、`vm/sched/` | `internal/{source, semantics, execution, process, hostbridge, stdlib, archcheck}` | — | D-own の名前（§2.1） |
| bind テーブル | 遅延構築して共有 | `NewSession` で bind を継承し、特殊形式は live で共有。Session ごとに変わる状態には factory が要る | bind テーブルがエンジンの状態を捕捉している（S6） | 明示的な `bind.Env` を factory の入力にした、レジストリ単位の遅延構築（§2.7） |
| 順序 | フェーズ 0〜5 | M0〜M6 | — | 1 本に統合した順序（§4） |

## 0. 変えてはいけないもの

| # | 不変条件 | 出典 |
|---|---|---|
| I1 | **Lazy Go**: パッケージは実行が到達したときに初めて locate、parse、index、initialize する。`go/types`、`go/packages`、`go list` は使わない | plan-minigo-vm §1, §4, §5、AGENTS.md |
| I2 | **全域コンパイラ**: コンパイラは AST を拒否しない。未対応の構文は `TRAP` にし、実行されたときだけ発火させる | plan-minigo-vm §3 |
| I3 | **スクリプトは valid Go**: スクリプトには gopls と vet が効く。インタプリタの拡張はスタブパッケージ（`minigo.dev/host`、`define`）で表す | plan-minigo-vm §1, §11 |
| I4 | **Quoted Go**: 特殊形式は canonical なシンボル同一性で実行時にディスパッチする。AST→AST マクロにはせず、スタブパッケージもロードしない | plan-minigo-vm §12 |
| I5 | **成功したふりをしない**: 黙って誤答するくらいなら trap する | todo-priority.md |
| I6 | **速度は目標にしない**: 単純さとカバレッジを実行速度より優先する。性能改善は、コードの単純化を伴う場合か、計測済みの病理を取り除く場合に限る | README |
| I7 | **公開埋め込み API の source 互換を保つ**: `NewEngine`、`Run`、`Call`、`Bind`、`RegisterSpecial`、`WrapFunc`、`ValueOf`、`NewSession`、`inspect`、`repl`、`storybook` の利用側（`examples/*` 全部）がそのままコンパイルできるか、機械的に移行できること | examples/ |
| I8 | **定義は共有し、状態は所有する**（D-own）: ソース由来のものは共有し、実行が作るものは 1 つの Session が所有する。宣言だけを問う問い合わせは、初期化も実行もしない | D-own §4〜§6 |

これ以外（パッケージ境界、VM の frame/stack レイアウト、型モデル、intrinsic テーブルの形）はすべて変えてよい。

### 0.1 用語

D-own の用語集に従う。この計画で特に重要なのは次のとおり。

- **Index**: パッケージごとの宣言表。
- **Resolve**: 文脈の中で宣言を見つけること。実行は含意しない。
- **Materialize**: 実行時の型、関数、値を作ること。必ず対象を明示する。既存の `ImportRef.Materialize` は実際にはパッケージを *index* しているので、アダプタでそれを明示する。
- **Initialize**: パッケージ変数と `init` を実行すること。
- **TypeRef**: 文脈付きの遅延な型式。
- **TypeID**: 実行時オブジェクトのアドレスに依らない型の同一性。
- **PackageImage**: 1 つのソース版についての不変な定義。
- **PackageInstance**: 1 つの Session における PackageImage の実行状態。
- **Engine / Session / Process / VM / Frame / Chunk / Storage**: 所有の単位。定義は D-own に従う。
- **Panic** はスクリプトが recover できる。**Trap** はできない。

単に「ロード」とは書かない。どの対象をどの段階まで進めるのかを書く。

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

### 1.2 構造上の問題（コストの大きい順）

1. **定義と実行状態が分かれていない**（D-own）。
   - `runtime.Package` は、ソース由来の定義（AST、index、import spec）と実行状態（globals、初期化状態、`sync.Once` でロード済みパッケージを保持する `ImportRef`）の両方を持っている。
   - そのため Session 間で安全に共有できるものがなく、キャッシュの所有者もどこにも書かれていない。
2. **型モデルが一つではない。**
   - `runtime.TypeDef` は遅延 AST 参照と解決済みの型を兼ねている。同一性は、VM に散らばったヒューリスティクス（`tdShapeEq`、`sameSpelledTwin`、`hostNilTwin`、`hostTwinIs`、`peelAlias`、`structFieldsEq`、`underlyingShape` など）で近似されている。
   - スパイク S7 によれば、これらは *別々の* 問いに答えている。
     - 5 つは host と source の twin を解消している。
     - 4 つは Go の「underlying が同一」と代入可能性の規則を実装している。

     同一性、代入可能性、変換可能性、インタフェース充足という 4 つの問いに分ける D-own の形が正しい。
   - twin は、bind されたパッケージが source 解釈のパッケージで宣言された型の host 値を返すときに生じる。実行時の合成（`hostTypedefOf`、`hostPkgElemTypedef`）は呼び出しのたびに TypeDef を作り、1 回のスイート実行で `ast.Expr` のコピーが 163 個できていた。
3. **静的型の知識がコンパイラと VM に分かれている。**
   - コンパイラ側には `staticOpTyp`、`callOpTyp`、`stampVarTyps`、`opTyp` がある。
   - VM 側には `OpLocalTyp`/`OpGlobalTyp` のスタンプと、呼び出しごとの `statics` がある。

   コンパイラが「何を知ってよいか」という契約には名前すら付いていない。
4. **コンパイル時の遅延性の契約が暗黙で、「不明」が nil で表されている。**
   - コンパイラは 8 箇所で `ImportRef.Materialize()` を呼び、`DisplayName` 経由で `runtime/typedef.go` の呼び出しにも到達する。
   - 計測では、コンパイルがパッケージを初期化したことはなく（違反 0 件）、パッケージのビルドのうち 6.5% はコンパイラから始まっていた。
   - 一部の答えは *意味論的* である（定数性、nil になり得るか、`==` の下ろし方を決めるオペランド型、リテラルのキーの型）。これらに答えないと 15 件のテストが壊れる（スパイク S2）。
   - 現状は nil や false が「存在しない」「失敗した」「まだ分からない」を兼ねており、D-own はこれを禁じている。
5. **VM が god object になっている。** `*VM` は次のものをすべて抱えている。
   - dispatch ループ、frame、defer/panic/recover
   - goroutine のスケジューリングとデッドロック検出
   - reflect 境界と script→host アダプタ
   - 型なし定数の演算
   - インタフェース充足判定
   - ジェネリクス推論
   - `specialCtx`
6. **`VMCaller` が 21 メソッドのインタフェースで、しかも隠れた表面がある。**
   - これを受け取る 360 関数のうち、96 は使わず、184 は受け渡すだけ。
   - 呼び出し側は型アサーションで `HostClear`、`HostMapDelete`、`HostNilTwin`、`MaterializeConstErr`、`CallCatching`、`ElemZero` を使っている。
   - `vm.vmOf` は 7 箇所で `*VM` にダウンキャストしている。
7. **intrinsic テーブルが 3,000 行の単一関数で、Engine ごと、Session ごとに作り直されている。**
   - `NewSession` 経由の最小の実行では、実行時間の 60〜70%、アロケーションの約 94% を占める。
   - `NewEngine` 経由では、代わりに `pkg/locator` の `go env` サブプロセス 2 つが支配的である。
8. **frame とローカル変数のレイアウトが構造上悲観的になっている。**
   - ローカル変数はすべてヒープ上の `*Cell` になる。
   - `OpLocalRef` はストア先に加えて、変数に対するすべての selector の読み出しと呼び出しレシーバにも出る（`OpLocalRef` 全体の約 70%）。その結果 `chunkCellsEscape` が true の chunk は 44.5% で、正確な規則なら約 10% で済む。
   - `OpNewLocal`/`OpRenewVar` は `:=` ごと、ループの反復ごとに新しい cell を確保する。

## 2. 目標アーキテクチャ

### 2.1 レイアウト（D-own の木に D-vm の中身を配置したもの）

```text
minigo
├── minigo.go, session.go   公開 API、組み立て、互換の窓口
├── internal
│   ├── source        resolve + ファイル選択 + ソース版 + PackageImage
│   │                 (AST, index, import spec)。公開後は不変で共有される
│   ├── semantics     宣言と型の問い合わせ: TypeRef, TypeID, 4 つの関係,
│   │                 メソッドセット, フィールドパス, ジェネリクス推論の単一化。
│   │                 runtime, vm, compile を import しない
│   ├── execution     Session, PackageInstance, 初期化の状態機械, materialize,
│   │                 TypeID → 実行時型の intern 表, 特殊形式のレジストリと context 実装
│   ├── process       goroutine, park/wake, デッドロック検出, コールバックのライフタイム
│   │                 (D-vm の vm/sched)
│   ├── hostbridge    reflect 境界, script→host アダプタ (生成), host↔script の値変換
│   │                 (D-vm の hostffi)
│   ├── stdlib        bind レジストリ + パッケージ単位の Install (§2.7)
│   ├── archcheck     依存と共有の lint (go/parser のみ)
│   └── cmd/archcheck
├── syntax  index  resolve  pkg/locator
├── compile           AST → bytecode; スロット分類; ヒントと事実 (§2.4)
├── bytecode          op, Chunk, CallSite 記述子, verifier, disasm
├── vm                dispatch ループ, frame, call, defer/panic/recover
├── runtime           公開の値と基本的な実行契約 (データのみ)
├── builtin           len, append, make, … (capability インタフェース)
├── special           公開: SymbolID, QuotedCall (値を参照しない型だけ)
├── inspect  minireflect  repl   semantics の利用側。独自のエンジンにはならない
```

責務と「含めてはいけないもの」は D-own §11 に従う。D-vm から次を加える。

- `vm` は `reflect` を import しない。host 値は不透明な `*runtime.GoValue` で、host とのやり取りはすべて `hostbridge` を通す。
- `semantics` が import するのは `syntax`、`index`、`internal/source` だけである。実行側の呼び出し元は `execution` のアダプタを通して使う。
- 実行時の値に対する型なし定数の演算は実行側（`vm` か `execution`）に残す。`semantics` が答えるのは「定数かどうか、宣言された型は何か」だけである。

### 2.2 `VMCaller` を capability インタフェースに分割する

*隠れたものも含めて* メソッドを小さなインタフェースに分ける。VM がすべてを実装し、利用側は必要最小限のものだけを受け取る。

```go
package runtime

type Caller interface   { Call(fn Value, args []Value) (Value, error); CallCatching(fn Value, args []Value) (Value, error); Recover() Value }
type Spawner interface  { Spawn(fn Value, args []Value) *Task; Task() *Task }
type TypeOps interface  { TypeOf(Value) *TypeDef; Zero(*TypeDef) Value; ElemZero(*TypeDef) Value; Convert(*TypeDef, Value) (Value, error); Equal(*TypeDef, Value, Value) bool; Copy(Value) Value; NewMap(*TypeDef) *Map; MapKeyOperand(*TypeDef, Value) Value }
type TypeResolver interface { ResolveType(from *TypeDef, x ast.Expr) (*TypeDef, error); FieldTypes(*TypeDef) ([]*TypeDef, error); ElemOf(*TypeDef) (*TypeDef, error); ArrayLenOf(*TypeDef) (int64, bool) }
type Members interface  { Member(base Value, name string) (Value, bool); MethodSetOf(Value) (map[string]bool, bool) }
type HostOps interface  { HostClear(Value) bool; HostMapDelete(m, k Value) bool; HostNilTwin(*TypeDef) Value; MaterializeConstErr(Value) error } // シグネチャは仮。現状は型アサーション経由
type Frames interface   { CallerPCs() []uintptr; CallerFrame(pc uintptr) (CallSite, bool); Package() *Package }

type VMCaller interface { Caller; Spawner; TypeOps; TypeResolver; Members; HostOps; Frames } // I7 のために残す
```

計測した利用状況: `Call` が 24 関数、`TypeOf` 16、`Convert` 13、`ElemZero` 12、`Zero` 10、`ElemOf` 6、残りは 5 以下。

移行の順序:

1. 受け渡し先の helper を先に型付けする。
2. caller を一切使わない 96 関数から引数を削る。
3. `vmOf` によるダウンキャスト 7 箇所を capability のメソッドに置き換える。

capability の呼び出しは、特定の Session と Process のために実行される。D-own §9 に従い、コールバックは「どの Session と Process に戻るか」「その Process の終了後にも走り得るか」を宣言する。

### 2.3 型モデル: `semantics.TypeID` + Session ごとの実行時 intern

```go
package semantics

// TypeRef は宣言の文脈を伴う遅延な型式。
type TypeRef struct { Expr ast.Expr; Ctx DeclContext } // パッケージイメージ, ファイルの import, 型引数束縛

// TypeID は実行時アドレスに依らない同一性。
//   source の named 型: (ソース版, 宣言パッケージ, 宣言位置, 型引数)
//   bind された host 型: *表している* 型の (PkgPath, Name, 型引数)
//   無名型:              構造的 (kind + 構成要素の TypeID)
type TypeID struct{ /* comparable */ }

type Result[T any] struct { Kind ResultKind; Val T; Err error } // Found | Missing | Deferred | Failed

type Service interface {
    TypeOf(ref TypeRef, p Policy) Result[TypeID]          // Policy: IndexedOnly | AllowIndex
    Identical(a, b TypeID) Result[bool]
    AssignableTo(v, t TypeID) Result[bool]
    ConvertibleTo(v, t TypeID) Result[bool]
    Implements(t, iface TypeID) Result[bool]
    MethodSet(t TypeID, p Policy) Result[*MethodSet]
    FieldPath(t TypeID, name string, p Policy) Result[[]int]
}
```

主な決定:

- **同一性のキーは TypeID にする。**
  - bind された named 型は、`reflect.Type` ではなく表している型をキーにする。`atomicBox` は `atomic.Value` と `atomic.Pointer` の両方を運び、`timerChanBox`/`afterFuncTimer` は `*time.Timer` を表しているからである。
  - 同じソース版の、bind された `time.Duration` と source 側で宣言された `time.Duration` は同じ TypeID を得る。
  - 異なるソース版の型を、パスと名前だけで同一視することはない（D-own §7）。
- **実行時の intern は Session ごとに行う。**
  - `execution` が `TypeID → *runtime.TypeDef` の表を持つ。Session の中では、host twin 系の helper（`hostTwinIs`、`hostNilTwin`、`sameSpelledTwin`、`HostNamedIs`/`HostTwinOf`、`sourceTwinOf`）がポインタの等価に置き換わる。
  - Session をまたぐとポインタは異なる。D-own はこれを明示的に許しており、比較は TypeID で行う。
  - host typedef を作る箇所（`hostTypedefOf`、`hostPkgElemTypedef`、`HostTypeOf`、インスタンス化によるコピー）はすべてこの表を通す。
- **4 つの関係は 4 つのまま。** `tdShapeEq`、`underlyingShape`/`convShapeEq`、`structFieldsEq` は、`semantics` の `AssignableTo`/`ConvertibleTo`/`Identical` になる。消えるのではない。`peelAlias` は `TypeOf` の解決の一部になる。
- **値の表現は 2 種類のまま。** 型を統合しても、値は `*runtime.Struct` としても `GoValue` としても持たれ得る。変換層（`hostErrTwins`、`goNative`、プロキシのボックス）は `hostbridge` に移し、値の種類ごとにコピーか共有かの契約を明示する（D-own §9）。
- `MethodSet` と `(TypeID, name)` ごとのフィールドパスはイメージ単位で一度だけ計算し、共有できる。これで「メソッドセットを二度歩く」と「selector 解決が名前で探し直す」の TODO が解消する。
- ジェネリクス推論は 2 つに分ける。現在の `inferCached` は実行時の引数の *値* と `statics` を受け取っている。
  - TypeID の上での単一化は純粋な規則なので、`semantics` に置く。
  - 引数の値から TypeID への対応づけは `runtime.Value` に触れるので、実行側のアダプタに残す。
  - インスタンスキャッシュのキーは、文字列で綴ったものではなく `(関数宣言, []TypeID)` にする。
- 表示（`%T`、`reflect.Type.String`）は、TypeID からではなく引き続き宣言から導出する。

### 2.4 コンパイル時の契約: ポリシー、結果、ヒントと事実

> **C1.** 関数のコンパイルでは、その関数内で構文上参照されているパッケージについて、ポリシー `AllowIndex` を使ってよい。初期化はせず、スクリプトのコードも host のコードも実行しない。（計測では現状でも違反 0 件。）
>
> **C2.** コンパイラは、コンパイラ用アダプタを介して semantics サービスにだけ問い合わせる。`compile/` から `ImportRef.Materialize()` を直接呼ばない。問い合わせは必ず Found / Missing / Deferred / Failed のいずれかを返し、nil で「存在しない」を表すことはない。
>
> **C3′.** コンパイル時の問いは、それぞれヒントか事実のどちらかである。
>
> - **ヒント** は速くするだけのもの（静的な引数 typedef、インラインキャッシュの初期値など）。Found でなければ捨てる。
> - **事実** は意味論を決めるもの: 定数性（シフト回数）、修飾された型が nil になり得るか、`==` の下ろし方を決めるオペランド型、リテラルのキーの型、スロット分類のためのレシーバの種類（§2.5）、gc の静的な拒否。結果の種類ごとに扱いを決める。
>   - Found: そのまま使う。
>   - Missing: 宣言が存在しないので、gc と同じメッセージの `TRAP` にする。
>   - **Deferred**: *遅延された事実* を出力する。これは `CallSite` に持たせた `TypeRef` で、VM が最初の実行時に `AllowIndex` で解決する。
>   - Failed: 原因を持った `TRAP` にする。semantics は原因を消さず、trap にするかどうかはアダプタが決める（D-own §6）。

ポリシー `IndexedOnly` の下では、まだ index されていないパッケージについての事実はすべて Deferred になる。そのため `IndexedOnly` が正しく動くのは、すべての事実に遅延経路ができてからである。スパイクで測った差: `IndexedOnly` 相当の oracle では現在 13 件、まったく答えない oracle では 15 件が失敗する。

`WithStrictLazyCompile()` は、`IndexedOnly` で、すべての事実を Deferred にしてコンパイルするモードとする。このモードで conformance スイートを回して結果が同一であれば、すべての事実に実行時の経路があることの証明になる。これを CI の同値性チェックとし、上の 15 件を受け入れ条件とする。

`compile/typehint.go` が `staticOpTyp`/`callOpTyp`/`opTyp`/`stampVarTyps` を置き換える。その出力は chunk に格納される命令ごとの情報（ヒント、事実、遅延された事実）であり、`OpLocalTyp` のプッシュや呼び出しごとの `statics` を置き換える。

### 2.5 VM の形

それぞれ独立して入れ、入れるたびに計測する。

1. **スロット分類。**
   - ローカル変数を `Plain`（frame のスロットに値を置く）か `Boxed`（`*Cell`）に分ける。
   - Boxed にするのは次の場合である。
     - 親の upval を経由しないクロージャにキャプチャされる。
     - `&x`/`&x.f`/`&x[i]` でアドレスを取られる。
     - ポインタレシーバのメソッドに束縛される。レシーバの種類は §2.4 の意味での事実であり、Deferred なら Boxed にする。
     - 特殊形式の呼び出し地点から見える。`trySpecial` が見えるローカルをすべてスナップショットし、`specialCtx.Eval` が呼び出し元の cell を共有するためである。
   - ストアには直接の op（`OpSetLocal`、`OpSetLocalIndex`、`OpSetLocalField`）を使い、Plain なローカルに対する selector の読み出しは `OpLocal` + `OpSelect` にする。
   - Plain なローカルでは、`OpNewLocal`/`OpRenewVar` がその場に書き込む。
   - 期待される効果: escape する chunk は約 45% から約 10% に減り、Boxed のまま残るローカルは約 5% になる。
   - 移植が必要な箇所（いずれもブロッカーではない）: `selectMember`/`structMember`、`assignRef`、`specialCtx.Resolve`、`localTypedefOf`、名前付き戻り値、range のイテレータのスロット。
   - 各 op は D-own §9 のコピー/共有表を契約として守る。struct と配列は Go のコピー地点でコピーし、スライスは backing array を共有し、map は共有し、ポインタと Storage は参照先の同一性を保つ。
2. **goroutine ごとに連続した値スタックを 1 本持つ。** frame はその中の窓 `(base, sp)` とする。`Chunk.MaxStack` は verifier が求め、引数はその場で渡す。
3. **`CallSite` 記述子。** 呼び出し地点ごとに不変な記録を 1 つ持ち、次のものをまとめる。
   - 静的な引数のヒント
   - spread 要素の型
   - quote インデックス
   - 遅延された事実
   - ソース位置

   `frame.argbuf/statbuf` と `deferredCall.statics/spreadTd` は不要になる。
4. **インラインキャッシュ**を `CallSite`/`Site` のスロットに atomic pointer で置く。
   - `OpGlobal` は解決済みの cell をキャッシュし、`Env` epoch でガードする。
   - `OpSelect` は `(TypeID → フィールドパス)` をキャッシュする。
   - 解決済みの遅延された事実も同じスロットを使う。
   - Session に依存する参照を持つキャッシュは Chunk と一緒にあるので、Chunk は Session をまたいで共有できない。コンパイル済みコードの共有は任意とする（§4 の M6）。
5. **bytecode verifier** をテスト時とデバッグオプション指定時に走らせる。スタック効果、ジャンプ先、スロット種別を検査し、`MaxStack` を求める。
6. **維持するもの**: `Value = any`、`Tuple`、Trap と Panic の区別、関数単位の遅延コンパイル、`SPECIAL_CALL`。tagged な `Value` はスコープ外とする（I6）。
7. **`OpEvalAST` を `OpEvalFragment` に改名**し、REPL/`EvalExpr` のブリッジとしてだけ残す。

### 2.6 実行状態と所有権（D-own の要約）

- `PackageImage` は `internal/source` に置く。公開後は不変で、同じソース版を使う Session 間で共有する。
  - 持つもの: AST、index、宣言位置、import *spec*、言語バージョンとビルド設定。
  - 決して持たないもの: 初期化の結果、グローバルの cell、`Function` オブジェクト、materialize された TypeDef、host を捕捉するクロージャ。
- `PackageInstance` は `internal/execution` に置く。Session ごとに持ち、globals、初期化状態、実行時の import インスタンス、materialize された関数と型を保持する。
  - ロード済みパッケージをキャッシュする `ImportRef` はインスタンス側の状態である。
  - 既存の `runtime.Package` は互換の窓口として残す。
- インスタンスの状態は、未初期化、初期化中、利用可能、失敗の 4 つ。
  - 同じ実行経路からの再入と、別の goroutine からの待機を区別する。
  - 部分的に初期化された状態での読み出しや循環についての既存の振る舞いは、変える前にテストで固定する。
- ソース版は、内容、ファイル集合、言語バージョン、ビルド設定を含む。
  - Session は取得した定義を保持し続ける。
  - 編集後に作られた Session は新しい版を取得する。
  - 稼働中の Session をその場で書き換えることはしない。
- host の登録: bind は `NewSession` で継承し、特殊形式の登録は live で共有する。この計画はどちらも変えない。登録をスナップショットするかどうかは、別の互換性の判断とする。

### 2.7 bind パッケージ: `internal/stdlib`

```text
internal/stdlib/
  registry.go   names: import path → メンバ名（安価なので即時に持つ）
                tables: import path → 初めて必要になったときに Session ごとに構築
  bind/         ヘルパ（fn, fnvc, fnvcRaw, hostType, …）と bind.Env
  strings/ bytes/ strconv/ fmt/ time/ os/ encoding/json/ text/template/ …
```

- 各サブパッケージは `Install(r *bind.Registry, env bind.Env)` を公開する。`bind.Env` は D-own §9 が求める Session ごとの factory の入力で、`fsPath`、`cwd`、`args`、出力先、`AllowedRoots` を持つ。
- **根拠。** 現状 `NewSession` は install 全体をやり直しており、それが最小のセッション実行の 60〜70% を占める。`NewEngine` については、`go env` サブプロセスのほうがコストが大きく、直すのも安い（§4 の M0）。
- **先に扱う結合**（スパイク S6）:
  1. `hostTwin` の `e.pkgs[rt.PkgPath()]` の検索で install を起動する。そうしないと黙って nil が返り、I5 を破る。
  2. `BoundPackages`、`BoundSymbols`、`BoundOf`、`vet`、`SourceOf`、`NewSession` の優先順位、bound-shadow チェックを、安価な `names` ビューに移す。
  3. エンジンが捕捉している状態を `bind.Env` に移す。
  4. グローバルな `templateFuncMap` をレジストリ単位にする。
- **生成。**
  - 869 エントリの約半分は、現在の `gen-intrinsics` で生成し、`// Code generated` ヘッダ付きでチェックインできる。
  - メソッドリストを持つ typedef（約 12%）は、先に生成器を拡張する必要がある。
  - 本物のアダプタ（約 25%）は手書きのまま残す。
  - 生成したラッパは呼び出しのたびに reflect を使う。I6 の下でこれを受け入れ、ホットなパッケージは直接のラッパを残してよい。
- `--src` が通るものについては、bind した各パッケージに source 解釈版との parity テストを付ける。
- `WithAllowedRoots`/`WithHostPolicy` はレジストリへのフィルタにする。

### 2.8 特殊形式: `special/`

- `SymbolID`、`QuotedCall` は公開パッケージ `special/` に置き、`runtime` にエイリアスを残す（I7）。`SpecialFunc`/`SpecialContext` はシグネチャが `runtime.Value`・`*Package`・`*TypeDef` を参照するため `runtime` に残す — `special` へ移すと runtime <-> special の循環になる。examples は `runtime.*` 経由で見るので影響しない。レジストリは `internal/execution.Registry` に置いたが、VM 側の context 実装（`vm/special.go` の `specialCtx`）は `*VM`/`*frame` を必要とし execution は runtime/vm を import できないため vm に残る。
- ディスパッチは、宣言パッケージ由来の canonical なシンボル同一性で行う。つまり import の別名、ローカルなシャドウイング、メソッドの宣言元をすべて考慮する。
- コンパイラがレジストリに問うのは `Has(SymbolID)` だけで、スタブパッケージは決してロードしない（I4）。
- 特殊形式の context が持つもの:
  - 構文上の位置と宣言の文脈
  - 型の問いのための semantics へのアクセス
  - それとは別の、呼び出し元の Session と Process に束縛された明示的な評価 capability

  `semantics` に `Eval` は追加しない。
- `SpecialContext.ResolveType` は `semantics.TypeRef`/`TypeID` を返す。

### 2.9 root パッケージはファサードになる

| 現在 | 移動先 |
|---|---|
| `builtins.go` | `builtin/` |
| `dispatch.go`（メソッドセット、埋め込み、iface） | `internal/semantics`（規則）+ `internal/execution` のアダプタ |
| `intrinsics.go`、`hostvars.go`、`hostslice.go`、`asmimpl.go` | `internal/stdlib/…` |
| `iterseq.go` | `internal/hostbridge` |
| `inspect.go` | `inspect/`（`semantics` の利用側） |
| `embed.go`、`parse.go`、`parsecache.go`、`SourceOf`/`LoadFile`/`LoadDirScope` の内部 | `internal/source` |
| `runtime/package.go` と `minigo.go` のライフサイクル | `internal/execution` |
| `vet.go` | `cmd/minigo/vet` |

root の型エイリアスで I7 を保つ。

## 3. 依存の目標

```text
syntax, resolve, pkg/locator           (互いのほかに内部依存なし)
index                → syntax
internal/source      → resolve, syntax, index, pkg/locator
internal/semantics   → internal/source, syntax, index          (runtime/vm/compile には依存しない)
bytecode             → syntax
runtime              → syntax, bytecode(Chunk のみ)             [データ]
compile              → bytecode, runtime, internal/semantics (アダプタ経由), special(Has のみ)
internal/process     → runtime
internal/hostbridge  → runtime, internal/semantics
vm                   → bytecode, runtime, internal/process, internal/hostbridge(インタフェース)
internal/execution   → internal/source, internal/semantics, runtime, compile, vm, special
builtin              → runtime (capability インタフェース)
special              → syntax（値を持たない識別子と引用呼び出しのみ）
internal/stdlib/...  → internal/stdlib/bind → runtime (capability), internal/hostbridge
minigo               → 上記すべて
inspect, minireflect, repl, storybook, cmd → minigo (inspect/minireflect は internal/semantics に問う)
```

`internal/source` は `semantics` に `IndexedOnly`/`AllowIndex` のビューだけを公開する。これで型レイヤは初期化を *呼べなくなり*、C1 が import グラフで強制される。

## 4. 移行: 統合した 1 本の順序

M は D-own のマイルストーン、P は D-vm のフェーズを指す。どのステップでも `make format lint test`、pin 済みの difffuzz ケース、examples を通す。共有状態やコールバックに触れるステップでは `make test-race` も走らせる。

| ステップ | 内容 | 完了条件 |
|---|---|---|
| **M0** + P0 | 振る舞いを固定する: 遅延性、実行なしでのメタデータ取得、Session の分離、編集後の更新、Quoted Go。`pkg/locator` で `go env GOROOT`/`GOMODCACHE` をメモ化する。`bytecode.Disasm` の golden を追加する。ベンチマークとスパイクのカウンタ（コンパイル起因のビルド、escape 率）を記録する。スパイクの 15 件を「コンパイル時の事実に依存すると分かっているもの」として pin する | 守るべき振る舞いと既知の差分を説明できる。ベースラインがある |
| **M1** | `sharedAssets` と `sessionState` に機械的に分割する。キャッシュのエントリまで再帰するフィールド許可リストを持つ `internal/cmd/archcheck` を、負例と正例付きで用意する | 所有権が構造に現れている。API と共有ルールは変えない |
| **P1** | `vm/vm.go`、`compile/compile.go`、`intrinsics.go` を既存のセクションに沿ってファイル分割し、gosimilarity R1〜R6 を取り込む。ロジックの変更はゼロで、`--color-moved` でレビューする | ファイルがサイズのラチェットの範囲に収まる |
| **M2** | `internal/semantics` を作る: 型宣言の問い合わせ、ポリシー `IndexedOnly`/`AllowIndex`、結果 Found/Missing/Deferred/Failed、コンパイラ用アダプタ。8 箇所（と `DisplayName` 経路）をヒントか事実に仕分けて、アダプタ経由にする。Deferred になった事実は、当面は今と同じく `AllowIndex` で即時に答える | 実際のコンパイラ経路が新しい問い合わせを使い、初期化を起こさない |
| **M3** | 軽量な所有権チェック（他の Session のインスタンスを初期化や呼び出しの前に拒否する）を、重い資産監査と分ける。D-own の計測ではリロード+呼び出しが約 2〜3 ms、AST/index の完全ハッシュ付きでは約 1〜1.6 s なので、ハッシュ監査はオプトインの実験として残す | 誤用を初期化前に捕捉でき、通常の利用はハッシュのコストを払わない |
| **P2** | 継ぎ目に沿って抽出する: capability インタフェース（受け渡し先の helper → 隠れたアサーション → `vmOf`）、`internal/process`、`special/`、`internal/hostbridge`（そのあとアダプタ一族を生成に切り替える）、`builtin/`、そして `internal/stdlib` を 3 段階（名前レジストリと `bind.Env` → `hostTwin` からの install → テーブルの遅延化）で進める | root と `vm.go` がそれぞれ約 3,000 行を下回る。archcheck が `vm/` での `reflect` を禁止する |
| **M4** + P3 | 型情報の検索を、機能グループごとに 1 PR ずつ統合する: フィールドと埋め込み → メソッドセットとインタフェース充足 → 同一性（TypeID、Session ごとの実行時 intern、host twin helper の削除）→ 代入可能性/変換可能性（shape 系 helper を `semantics` へ）→ `[]TypeID` をキーにしたジェネリクス推論。そのあと遅延された事実と `WithStrictLazyCompile` を CI に入れ、15 件を受け入れ条件にする | 利用側が切り替わり、古いロジックが消え、strict モードで同値になる |
| **M5** | `PackageImage`/`PackageInstance`、ソース版、派生キャッシュの所有を具体化する。配列長の畳み込み（`runtime.arrayFolds`）のようなグローバルな派生情報を明示的に扱う。特定の AST を再 parse する既存の特例は、その理由を確認せずには取り除かない | 再利用と無効化を、所有者と版で説明できる |
| **P4** | VM の形を次の順に進める: verifier + `MaxStack` → スロット分類（ストア、読み出し、レシーバ。`OpNewLocal`/`OpRenewVar` はその場書き込み）→ 連続スタック → `CallSite` 記述子 → インラインキャッシュ | frame と selector に関する性能系 TODO 5 件がクローズされるか意味を失う。escape する chunk が約 10% |
| **M6**（任意） | コンパイル済みコードの共有を検討する。Chunk は可変の解決キャッシュ、関数と型の参照、ジェネリクスの束縛を持つので、ソース共有の自然な延長ではない。シンボル単位の materialize、Session 間での bind テーブル共有、tagged `Value`（既定は「やらない」）もここで検討する | 判断が記録されている |

M0〜M3 が最初のマイルストーンで、これは D-own と同じである。P1 は機械的な作業で M2 の差分を読みやすくするので、この中に入れる。

## 5. リスクと緩和策

| リスク | 緩和策 |
|---|---|
| 大規模な移動が進行中の PR と衝突する | P1 は短い期間に区切ってファイル単位で入れる。移動と編集を同じ PR に混ぜない |
| intern で `%T` や `reflect.Type.String` が変わる | 表示は宣言から導出する。difffuzz の `%T` プローブで固定する |
| host の運搬型を共有する別々の型を統合してしまう | TypeID は表している型をキーにする。`atomic.Value` と `atomic.Pointer` の pin 済みケースを追加する |
| コンパイル時の答えが意味論的である（I5） | C3′: 事実は答えるか、遅延させるか、trap にするかのいずれかで、決して捨てない。`WithStrictLazyCompile` で、すべての事実に実行時の経路があることを確かめる |
| 遅延された事実でエラーがコンパイル時から実行時に移る | I2 の下で許容する。メッセージの文言は `gate_*` ケースで固定する |
| Session の状態が共有資産に漏れる | archcheck の再帰的なフィールド許可リスト、M3 の所有権チェック、レビューチェックリスト（D-own §16） |
| 資産監査が呼び出しごとには重すぎる | オプトインにとどめ、既定は所有権チェックにする（計測で 2〜3 ms 対 1〜1.6 s） |
| 遅延 stdlib で、他のパッケージから来た host 値の bind 済み typedef を見落とす | `hostTwin` の検索で install を起動する。`time` を import せずに `ctx.Deadline()` → `time.Time` を流すテストを追加する |
| 遅延 stdlib で初期化順や副作用が変わる | bind は純粋な構築処理だけにする。2 つのエンジンと 1 つの `NewSession` が同一の bind パッケージを見ることを確かめるテストを追加する |
| 共有 chunk 上のインラインキャッシュの並行性 | サイトごとに atomic pointer を使う。`make test-race` と `make concurrency-stress` を P4 のゲートにする |
| リポジトリ外の `VMCaller` 利用者 | `VMCaller` は union として残す |

## 6. 明示的な非目標

- 静的型検査器を導入すること、またはコンパイル時に型エラーのあるプログラムを拒否すること（I1/I2）。遅延された事実は、拒否するのではなく実行時に trap する。
- `semantics` に `Eval` を加えること。
- AST→AST マクロ（I4）。
- JIT、レジスタ VM、tagged value への全面書き換え（I6）。
- スクリプトの規約や `minigo.dev/host` スタブ API の変更（I3）。
- Session 間の OS レベル・アプリレベルの隔離。明示的に共有された host の map、ポインタ、クロージャは共有のままである。
- 決定的なスケジューラや、Go の並行性意味論の完全な再現。
- ポインタによるプロセス全体での型同一性や、コンパイル済みコードの共有（任意、M6）。

## 7. レビューチェックリスト

この計画のすべての PR に、D-own §16 の 9 つの問いと、スパイクから加えた 2 つを適用する。

1. 追加する情報は、ソース由来の定義か、実行が作る状態か。
2. このオブジェクトやキャッシュは誰が所有し、いつまで生きるか。
3. 宣言だけを問う問い合わせが、初期化と host の実行から離れているか。
4. 「まだ分からない」を「存在しない」や「型エラー」と区別しているか。
5. 宣言パッケージ、ファイルの import、型引数、ソース版の文脈を保っているか。
6. 同一性、代入可能性、変換可能性の違いを保っているか。
7. script/host 境界でのコピーと共有の契約が明示されているか。
8. 初回実行、別の Session、ソースの編集、並行実行のどれでも成り立つか。
9. lint、実行時チェック、振る舞いテストのどれでこの規則を強制するか。
10. *（スパイク）* このコンパイル時の問いはヒントか事実か。事実なら Deferred の経路があるか。
11. *（スパイク）* TypeID の intern 表を迂回する実行時の合成箇所を追加していないか。

## 8. 衝突の判断: D-vm 対 D-own

既定の勝者は置かず、各衝突を次の 4 つの観点で判断した。

- **E**: スパイクの証拠
- **I**: 不変条件 I1〜I8 との整合
- **S**: 何が単純になり、何が消えるか
- **M**: 移行コストとリスク

| # | 論点 | D-vm | D-own | 比較 | 採用 |
|---|---|---|---|---|---|
| K1 | コンパイラが答えられないときどうするか | unknown は常に安全で、VM が動的に判断する（C3） | 4 種類の結果。Deferred なら遅延参照か実行時解決を残す | **E** が D-vm を否定した（誤出力 4、偽の trap 1、拒否の喪失 9）。D-own の Deferred は正しいが、*どの* 問いに実行時の経路が要るかは言っていない | **D-own** に、スパイクのヒント/事実の区別を加える。どちらか単独では足りなかった |
| K2 | strict モード | 「すでに `Indexed` のものにだけ答える」。結果は同一でなければならない | 問い合わせのポリシーとして `IndexedOnly` があり、同値性は主張していない | 仕組みは同じ。**E**: 遅延された事実がなければ、どちらも 13 件が失敗し、ロード順に依存する | **統合**。ポリシーは `IndexedOnly` とし、同値性はすべての事実に Deferred の経路ができてから初めて主張する |
| K3 | 型の同一性 | `reflect.Type` ごとに 1 つ intern した `*Type`、どこでも `==` | 宣言元ソース、位置、型引数、ソース版からなる `TypeID`。ポインタは Session ごとに異なってよい | **E** は `reflect.Type` をキーにすることを否定した（`atomicBox`、タイマーのボックス）。D-vm のポインタ `==` は Session やソース版をまたげないが、D-own の TypeID はまたげる。1 つの Session の中では D-vm のほうが安い（**S**） | 同一性は **D-own**。D-vm の intern は、D-own のキーの下での高速経路として、Session ごとの `TypeID → *TypeDef` の表に残す |
| K4 | twin 系と shape 系の helper | すべて `==` になる | 見た目が似ているだけでは統合しない。同一性、代入可能性、変換可能性、充足判定は分ける | **E**: `==` になるのは 9 中 5 で、残り 4 つは別々の Go の規則を実装している。D-vm は言い過ぎだった | **D-own** |
| K5 | 型レイヤが `runtime` を import するか | する: `types → runtime`。段階的に移行するための `Def *runtime.TypeDef` の逆参照あり | しない: `semantics` は runtime、VM、compiler に依存しない | D-own なら C1 が import グラフで強制され、inspect/minireflect が VM なしで答えを共有できる（**I**、**S**）。D-vm のほうが移行は安い（**M**）。スパイクでこれらの答えが意味論を決めると分かった（S2）ので、VM なしでテストできるレイヤにはアダプタのコストを払う価値がある | **D-own**。D-vm の移行手法（呼び出し箇所ごとに移す。`TypeDef` に `TypeID` フィールドを持たせる）は残すが、semantics 内の逆参照ではなく実行側のアダプタとして使う |
| K6 | ジェネリクス推論をどこに置くか | まるごと `types/infer.go` | semantics を機能グループごとに育てる | `inferCached` は実行時の値を受け取っているので、そのままでは純粋な semantics には置けない | **分割**する（§2.3）: 単一化は semantics、値 → TypeID はアダプタ。どちらの案にもなかった |
| K7 | 型なし定数の演算 | `vm` の下に独立した `constant/` パッケージ | semantics は実行に必要なことにだけ答え、評価はしない | 実行時の `UConst` 値に対する演算は評価であって、宣言の問いではない | **D-own の境界**。演算は実行側に残し、`semantics` は「定数か、宣言された型は何か」だけに答える |
| K8 | パッケージのレイアウト | 公開の `types/`、`loader/`、`hostffi/`、`stdlib/`、`vm/sched/` | `internal/{source, semantics, execution, process, hostbridge, stdlib, archcheck}` | `internal/` にすれば、移行途中の API が I7 の義務にならない（**I**、**M**）。D-vm の名前は仕組みを表し、D-own の名前は所有を表すので、レビューチェックリストと揃う | **D-own**。例外として `special` は公開のまま残す（I7）。ただし `special` に入るのは値を参照しない `SymbolID`/`QuotedCall` だけで、`SpecialFunc`/`SpecialContext` は runtime 値を参照するため `runtime` に残す（移すと循環依存）。examples は `runtime.*` の公開面を使う。レジストリの実装は `internal/execution`、VM 側の context 実装は `vm` に置く |
| K9 | bind テーブル | 遅延構築し、エンジン間で共有し得る | `NewSession` で bind を継承する。Session ごとに変わる状態には明示的な factory が要る | **E**: 約 45 個のクロージャがエンジンの状態を捕捉しているので、その状態を移さない限り共有はできない。D-own の factory がまさに欠けていた部品である。D-vm の遅延化は、セッションのコスト（60〜70%）が求めているもの | **両方**。D-own の所有モデルを、`bind.Env` を factory として採り、D-vm のパッケージ単位の遅延構築と組み合わせる。エンジン間の共有は M6 に送る |
| K10 | アーキテクチャの強制 | `archtest_test.go`: import グラフとファイルサイズのラチェット | `internal/cmd/archcheck`: import グラフ、可変グローバル、共有資産に対する再帰的なフィールド許可リスト | D-own は、Session の状態が共有資産の *中に* 漏れることを検出できる。これは import グラフでは見えない。D-vm のサイズのラチェットは別の観点である | **D-own** に、D-vm のファイルサイズのラチェットを加える |
| K11 | 実行時のチェック | なし（レビューとテスト） | 既定は軽量な所有権チェック、重い資産監査はオプトイン（計測で 2〜3 ms 対 1〜1.6 s） | D-vm にはここが何もない。D-own の計測値が分割を正当化している | **D-own** |
| K12 | 作業の順序 | ファイル分割 → パッケージ抽出 → 型モデル → VM の形 | M0 固定 → M1 所有権の分割 → M2 semantics → M3 チェック → M4 統合 → M5 image/instance → M6 任意 | D-vm は所有権の境界ができる前にパッケージを抽出するので、それを後でまた改名・移動することになる。D-own は VM の形と stdlib のコストを扱っていない。スロット分類はレシーバの種類という *事実* を必要とする（§2.5）ので、M4 の後でなければならない | **D-own の骨格**。D-vm の P1 は最初のマイルストーンの中に入れ（機械的で M2 のレビューを楽にする）、P2 は M3 の後、P4 は M5 の後に置く（§4） |
| K13 | コンパイル済みコードの共有 | 暗黙に前提している（atomic なインラインキャッシュを持つ共有 chunk） | 任意の M6。Chunk は Session に依存する参照を持つ | 解決済みの cell を持つインラインキャッシュは Chunk を Session 依存にする。D-vm はこれを見落としていた | **D-own**。M6 で判断するまで Chunk は Session ごととする。D-vm の atomic なキャッシュは、1 つの Session 内の goroutine 間のために残す |

D-vm を採用した、または残した領域:

- VM の形（§2.5）。D-own は frame、スロット、スタックを扱っていない。
- capability インタフェース（§2.2）。
- stdlib の遅延構築と生成の計画（§2.7）。
- `go env` のメモ化。
- verifier と Disasm の golden。
- `special` を公開のまま残すこと。

これらの領域では D-own は何も言っていないか、矛盾しない。そしてスパイクは、修正を加えたうえで D-vm を支持している。
