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

## 守ること

- `runtime.Value` を受け取らない。実行もしない。`Eval` は追加しない。
- 同一性、代入可能性、変換可能性、インタフェース充足は別々のメソッドにし、1 つの `Equal` や `Compatible` にまとめない。
- M2 で実装するのは、最初の 1 つの問い（その名前が宣言している型式は何か）だけでよい。残りは M4 で機能グループごとに足す。
- テストでは、4 種類の結果それぞれについて、どのパッケージが index され、何が初期化されなかったかを確かめる。
