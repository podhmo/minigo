# stdlib の遅延構築

[../TODO.md](../TODO.md) の P2-10〜P2-14 から参照する。

## 規模

- bind している import パスは 45 個（host と inspect がそれぞれ 2 つのパスで bind されているので、異なるものは 41 個）、メンバは 1,134 個。
- `installStdlib`（intrinsics.go:69〜3127、約 3,060 行）の中のメンバエントリは 869 個。
- testdata と examples の 944 ファイルが直接 import している bind 済みパッケージは、平均 1.92 個。148 ファイルは 1 つも import していない。`fmt` は 726 ファイル、`reflect` は 233 ファイルが import している。

## 結合（遅延化の前に扱うもの）

1. **`hostTwin`（minigo.go:314）**: `e.pkgs[rt.PkgPath()]` で、他のパッケージから流れてきた host 値の bind 済み typedef を探している。現状は全パッケージが bind 済みなので見つかる。遅延化した後は、ここで install を起動しないと黙って nil が返る。
2. **即時に読んでいる箇所**: `BoundPackages`/`BoundSymbols`/`BoundOf`（minigo.go:828〜846）、`vet.go:196`、`SourceOf`（minigo.go:989）、`NewSession` の bind の優先順位（minigo.go:455〜470）、`loadPathMode` の bound-shadow チェック。
3. **エンジンの捕捉**: 約 45 箇所で `e.` を直接使っている。`e.fsPath`（17 箇所）、`e.cwd`（5 箇所。`os.Chdir` が書き込む、intrinsics.go:1629）、`e.args`、`e.cwdAbs`、unsafe/slices/maps の helper、fmt/os 用の `h.out()`。`os` と `host` のテーブルの中身は `e.cfg.AllowedRoots` で決まる（intrinsics.go:1721, 1876, 1931）。inspect は意図的にセッションのエンジンを捕捉している（minigo.go:459 のコメント）。
4. **グローバルな `templateFuncMap`**（intrinsics.go:3137）: text/template（:3070）と html/template（:3104）、そして全エンジンで共有されている。`Bind` は `td.Pkg` が nil のときだけ設定する（minigo.go:787）ので、最初のエンジンのパッケージが残り続ける。
5. **共有して安全なもの**: `proxyHostTypes`（intrinsics.go:3362）と `hostErrTwins`（:8810）はグローバルだが、共有しても安全である。

## 生成できる割合（869 エントリの内訳）

| 種類 | 概数 | 扱い |
|---|---|---|
| 生成できる形 | 約 440（約 50〜55%） | `h.fn*` の 1 文ラッパ約 300、`&GoValue{V: pkg.F}` 62、定数と変数約 90 |
| typedef | 約 112（約 12%） | `hostType` 67、TypeDef リテラル 45。メソッドリスト、`Anon` の AST、`HostScalar` を持つものは生成器の拡張が必要 |
| 手書きのアダプタ | 約 200（約 25%） | 生の `BuiltinFunc` 72、`atomicOp` 39、複数行の `h.fn*` 約 70、scan/sort/slices/`ioCall`/split-func/`templateMust`/`osErrPred`/`randBig` など |

`gen-intrinsics`（cmd/minigo/main.go:783〜940）は、関数ごとに `WrapFunc`、変数と定数ごとに `ValueOf`、構造体ごとに `HostTypeOf` を出力する。ジェネリックな関数と型はスキップする。`WrapFunc` は呼び出しのたびに reflect を使うので、現在の直接ラッパより遅い。
