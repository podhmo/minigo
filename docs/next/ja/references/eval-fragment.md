# OpEvalFragment の用途（P4-6）

通常のコンパイルが `OpEvalFragment` を発行する箇所は3つある。

- 型 selector（`compile/expr.go`）：`pkgScopeTyp` があるとき `pkg.Type` を宣言ファイルの import で解決する。
- 複合リテラルのキー（`compile/lit.go`）：fold が見えなかった位置キーを宣言スコープで評価する。
- 配列長（`compile/lit.go`）：`emitLenFolds` が `pkgScopeTyp` 下の長さ式を同じく宣言スコープで評価する。

## 決定：通常実行の経路に残す

3箇所とも目的は同じで、「式が属する宣言ファイルのスコープ（globals + そのファイルの import）で実行時に評価する」こと。利用側（use site）は pkg を import していなかったり、同名を shadow していたりするため、use site のスコープでは評価できない。

専用の宣言スコープ解決経路へ移す代替案（コンパイル時に fragment をコンパイルして chunk に埋め込む）は成立しない。fragment が参照する名は宣言パッケージの実行時スコープを必要とする（パッケージ変数・import 解決は lazy strict で Ready になるまで確定しない）。コンパイル時に評価してしまうと「lazy な初期化順序」の前提と反するので、実行時評価のまま残すのが正しい。

## 付随する変更

これまで `OpEvalFragment` は実行のたびに `CompileExpr` で fragment を再コンパイルしていた。fragment のコンパイル結果は site ごとに不変なので、`bytecode.ASTFragment` に `Cache atomic.Value` を持たせて最初の実行で memoize する（CallSite.Cache と同じ構造）。
