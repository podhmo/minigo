# コンパイル時の事実に依存するテスト（受け入れセット）

[../TODO.md](../TODO.md) の M0-7、M4-7、M4-8 から参照する。

スパイクでコンパイラの oracle を「一切答えない」（`none`）にしたときに新たに失敗したテストである。ここが C3′ と `WithStrictLazyCompile` の受け入れ条件になる。`peek`（すでに materialize 済みのものにだけ答える）でも、★印以外の 13 件が失敗した。

| テスト | 種類 | 症状 | 依存している事実 |
|---|---|---|---|
| `TestDiffRegressions/assign_pkgname_differs` | 誤出力 | 1 行が出力されない | パス末尾と異なるパッケージ名での import 解決（`pkgImportRef`） |
| `TestDiffRegressions/litkey_named_peel` | 誤出力 | map のキーが `kk` ではなく `k` になる | 複合リテラルのキーの型（`selTypeSpec`） |
| `TestDiffRegressions/src_pkgtype_maplit_keys` | 誤出力 | `bad-name` が `name` になる | 同上 |
| `TestDiffRegressions/shift_const_count` ★ | 誤出力 | `true 1.8e19` が `false 0` になる | シフト回数の定数性（`isConstExpr` → `ShiftConstCount`） |
| `TestNetHTTPRoundtrip` ★ | 偽の trap | `rc == NoBody` が `mismatched types io.ReadCloser and noBody` で trap する | `==` を interface 比較に下ろすためのオペランドの静的型（`selPkgMemberOpTyp`） |
| `TestDiffRegressions/gate_qualconst` | gc の拒否が失われる | `mismatched types D and time.Month` が出ない | 修飾された定数の型 |
| `TestDiffRegressions/gate_qualconstbind` | 同上 | `mismatched types rune and int` | 同上 |
| `TestDiffRegressions/gate_qualforeign` | 同上 | `mismatched types time.Duration and int64` | 修飾された型 |
| `TestDiffRegressions/gate_qualforeign_case` | 同上 | `mismatched types int64 and time.Duration` | 同上 |
| `TestDiffRegressions/gate_qualforeign_mon` | 同上 | `mismatched types time.Month and int` | 同上 |
| `TestDiffRegressions/gate_qualforeign_nil` | 同上 | `mismatched types time.Duration and untyped nil` | nil になり得るか（`selNilable`） |
| `TestDiffRegressions/gate_qualscalbind` | 同上 | `mismatched types time.Duration and int64` | 修飾された型 |
| `TestDiffRegressions/selconv_typearg` | 同上 | `mismatched types time.Duration and MyDur` | 型引数としての修飾型 |
| `TestDiffRegressions/selnilable_trap` | 同上 | `mismatched types time.Duration and untyped nil` | nil になり得るか |
| `compile.TestSelMemberSyntheticTypeEntry` | ユニットテスト | is-type の判定が失われる | `selMember` を直接テストしている |

M4-7 を終えた時点で、これらがすべて「強制 Deferred」のモードでも通ることを確かめる。gc の拒否は、コンパイル時ではなく実行時の `TRAP` になってよい（I2）。ただしメッセージは同じでなければならない。
