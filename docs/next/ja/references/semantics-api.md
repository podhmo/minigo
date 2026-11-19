# semantics の API（草案）

[../TODO.md](../TODO.md) の M2-1 から参照する。計画書 §2.3 と §2.4 の型を、実装の出発点としてまとめたもの。

```go
package semantics

type Policy int

const (
	IndexedOnly Policy = iota // すでにある index にだけ答える
	AllowIndex                // locate/parse/index までは要求してよい。初期化しない
)

type ResultKind int

const (
	Found ResultKind = iota
	Missing  // 範囲内に宣言がない
	Deferred // まだ答えがない。不正な Go ではない
	Failed   // 取得または問い合わせ自体の失敗。Err に原因がある
)

type Result[T any] struct {
	Kind ResultKind
	Val  T
	Err  error
}

// DeclContext は名前を解決する文脈。宣言パッケージ、ファイルの import 表、型引数の束縛。
type DeclContext struct { /* image, file, binds */ }

type TypeRef struct {
	Expr ast.Expr
	Ctx  DeclContext
}

type TypeID struct { /* comparable。typeid.md を参照 */ }

type Service interface {
	TypeOf(ref TypeRef, p Policy) Result[TypeID]
	Identical(a, b TypeID) Result[bool]
	AssignableTo(v, t TypeID) Result[bool]
	ConvertibleTo(v, t TypeID) Result[bool]
	Implements(t, iface TypeID) Result[bool]
	MethodSet(t TypeID, p Policy) Result[*MethodSet]
	FieldPath(t TypeID, name string, p Policy) Result[[]int]
}
```

## 判定の配置（M4-5 の決着）

VM から抽出した型判定（同一性・代入可能性・変換可能性）は `*runtime.TypeDef` と
`TypeResolver` フックの上にある typedef 層の比較器であり、宣言層の `TypeRef`/`TypeID`
を受け取る実装ではない。semantics は `internal/index` だけに依存し、`runtime` を
import しないので、これらを移すには typedef の写しや解決機構の重複が要る。
そこで現行の境界を採用する: **比較器は `runtime` に残し、`TypeResolver` フック経由が
実行側アダプターとする**。宣言側の呼び手（M4-6 の unification）が出た時点で、
`TypeRef`/`TypeID` を受け取る判定をここの `Service` に足す。

利用側の記録（`:func` は定義位置、あとは主な呼び出し元）:

| 判定 | 実装（runtime） | 利用側 |
| --- | --- | --- |
| 同一性 | `TypIdentical` / `TypIdenticalStrict`（typedef.go） | `ShapeEq.Same`・`ConvertibleTo`・`namedTypEq`・intrinsics の `reflect.DeepEqual` 経路・`repl/complete` |
| 代入可能性 | `ShapeEq`（assignable.go、`TypeResolver` 注入） | `vm/assert.go` の `tdShapeEq`/`tdShapeEval`・`PointeeAlias`・`dispatch.go` の convShapeEq |
| 変換可能性 | `ConvertibleTo`（convertible.go） | `vm/convert.go`・`minireflect/rtype.go`・`dispatch.go`。host 値の `reflect.Type.ConvertibleTo` は別経路（hostbridge・stdlib bind・iterseq） |
| 構造体形状 | `StructFieldsEq`（assignable.go） | `vm/assert.go`・`vm/convert.go`・`ConvertibleTo` |
| 綴り | `TypSpelling`/`TypGoSpelling`/`TypUnderlyingSpelling`（typedef.go） | repl 補完・`minireflect`・`typeid.go`・`hostslice.go` |
| alias/underlying 解決 | `TypeResolver` フック（`PeelAlias`/`Underlying`/`ElemOf`） | `dispatch.go` の `aliasOf`/`underlying`/`elemOf` |

移設する場合の分割（計画の M4-5a/b/c 相当）:

- 同一性とエイリアス: `Identical` + `TypeOf` の alias 解決（定義型・エイリアス・構造体タグ・パッケージ固有名）。
- 代入可能性: 上の結果を使って `AssignableTo`（型なし定数・named/unnamed・インタフェース充足の照合）。
- 変換可能性: `ConvertibleTo`（構造・タグ・数値など代入と結果が分かれるケース）。

## 守ること

- `runtime.Value` を受け取らない。実行もしない。`Eval` は追加しない。
- 同一性、代入可能性、変換可能性、インタフェース充足は別々のメソッドにし、1 つの `Equal` や `Compatible` にまとめない。
- M2 で実装するのは、最初の 1 つの問い（その名前が宣言している型式は何か）だけでよい。残りは M4 で機能グループごとに足す。
- テストでは、4 種類の結果それぞれについて、どのパッケージが index され、何が初期化されなかったかを確かめる。
