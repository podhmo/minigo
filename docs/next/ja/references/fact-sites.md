# コンパイラの問い合わせ箇所: ヒントと事実の仕分け

[../TODO.md](../TODO.md) の M2-3、M2-4、M4-7 から参照する。M2-3 で確定した仕分け。各箇所の関数コメントにも `semantics: fact` / `semantics: hint` と記した（ファイル分割後の位置、スタック #1113 時点）。

C3′ の定義:

- **ヒント**: 速くするだけのもの。Found でなければ捨ててよい。
- **事実**: 意味論を決めるもの。答えるか、Deferred として実行時に解決するか、Missing/Failed なら `TRAP` にする。決して捨てない。

| 箇所 | 関数 | 何を決めているか | 分類 | 根拠 |
|---|---|---|---|---|
| compile/typehint.go:552 | `selPkgMemberOpTyp` | `pkg.Name` の静的なオペランド型 | **事実**（一部はヒント） | `==` の下ろし方と gc の拒否を決めている（`TestNetHTTPRoundtrip`、`gate_qual*`）。型付き定数のデフォルト化に使うだけの経路はヒントにできるかもしれない |
| compile/constfold.go:144 | `isConstExprDepth` | `pkg.C` が定数かどうか | **事実** | シフト回数の定数性（`ShiftConstCount`）と、定数呼び出しのインライン化を決めている（`shift_const_count`） |
| compile/constfold.go:286 | `certainlyNonConstDepth` | `pkg.V` が確実に定数でないかどうか | **事実** | `ShiftNonConstCount` と、非定数シフトの左オペランドの判定を決めている |
| compile/typehint.go:1434 | `pkgDispName` | 診断メッセージの中のパッケージ名 | **事実（メッセージのみ）** | 意味論は変えないが、gc と同じ文言を出すのに必要。Deferred のときは実行時の trap で同じ名前を出す |
| compile/typehint.go:1491 | `selTypeSpec` | `pkg.T` の TypeSpec | **事実** | 複合リテラルのキーの型と変換を決めている（`litkey_named_peel`、`src_pkgtype_maplit_keys`） |
| compile/typehint.go:1508 | `pkgImportRef` | パッケージ節の名前による import の解決 | **事実**（名前解決） | パス末尾と名前が違うときの解決そのもの（`assign_pkgname_differs`） |
| compile/typehint.go:1718 | `selMember` | `pkg.T` が型か、その spec と typedef | **事実** | `selIsType`、`selIsIface`、`selNilable` が使っている |
| compile/typehint.go:1790 | `selNilable` | `pkg.T` が nil になり得るか | **事実** | `x == nil` を gc と同じく拒否するかどうかを決めている（`selnilable_trap`、`gate_qualforeign_nil`） |

ほかに、`DisplayName` 経由で到達する `runtime/typedef.go` の修飾子の綴り（`DisplayName` 内のパッケージ名修飾ロジック）は、表示にだけ使われるのでヒント相当である。ただし文言は gc と揃える必要がある。

## 観察

- 8 箇所のうち、純粋なヒントと言えるものはほとんどない。現状のコンパイラの問い合わせは、ほぼすべてが事実である。
- したがって M4-7（遅延された事実）は、どれか一部ではなく全体に必要になる。M2-4 の段階では、Deferred を受け取ったら現状どおり `AllowIndex` で即時に答える、という移行用の挙動にしておく。
- 型付きの static 情報（`statics`、`OpLocalTyp`）のうち推論のデフォルト化に使うものはヒント寄りだが、型なし定数のデフォルト化が出力を変える場合は事実になる。M4-9 で個別に確認する。
