# stdlib の遅延構築

[../TODO.md](../TODO.md) の P2-10〜P2-13 から参照する。

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

## P2-12b（internal/stdlib/<path> の Install）

- `internal/stdlib` に共有バインド機構を移設: `WrapFuncWith`/`WrapBind`/`ScriptVal`/`ErrVal`/`NamedSized`/`StrsSlice`/`RuneSlice`/`BytesSliceOf`/`HostType`/`SliceOf` + `type Bind func(path, symbols)`。ルートの `native.go`/`intrinsics.go` は薄いラッパだけを残し、既存の呼び出しサイトは無変更。
- `internal/stdlib/html`・`internal/stdlib/utf8` がそれぞれ `Install(bind)` で `e.bindBound` 経由の遅延登録を実行（stdlibreg.go の pending.Register から呼ぶ）。`unicode/utf8` の手書き `EncodeRune`/`AppendRune`（スクリプトのスライス backing へ直接書く gc 互換シグネチャ）も同パッケージへ移動。`int64Of` は `hostbridge.Int64Of` を利用。
- genbind の spec に `Dir` フィールドを追加: 設定時は `<dir>/gen.go`（package = ディレクトリ名、`stdlib.WrapBind`/`stdlib.HostType` 参照）を出力する。`html`・`unicode/utf8` の生成テーブルは新パッケージに再生成済み。
- archcheck: `internal/stdlib→{internal/hostbridge, runtime}`、`internal/stdlib/html→{internal/stdlib, runtime}`、`internal/stdlib/utf8→{internal/hostbridge, internal/stdlib, runtime}` の辺を追加。

## P2-12c（状態を持つ installer の移設）

- `internal/stdlib` に `Helpers` インターフェースを追加（`Fn`/`Fnvc`/`FnvcRaw`/`Fn1`/`Fn2`）。`*hostHelpers` の同名メソッドを公開にして下限側は薄い委譲ラッパとして残し、Engine 面を `bind.Env` 経由の最小形に揃えた。
- `time` → `internal/stdlib/time`: `chanFeed`/`feedPump`/timer・ticker プロキシ/`afterFuncTimer`/`proxyHostTypes` をまとめて移設。`proxyHostType`/`proxyUnwrapHost`/`proxyHostTypeOf` は型の公開名解決として `ProxyHostType*` で公開し、ルートはラッパ経由（reflect ファサード・`%T`・typeid の既存経路を維持）。`vm.RegisterWakeChan`/`NoteExternalWait`/`SetWakeChanDead` は引き続き直に利用（`internal/stdlib/time→vm` 辺を追加）。
- `context` → `internal/stdlib/context`: `LockerOf`/`SyncOnceWrap`/`AsCtx`/`ScriptCoro`（`Ch` フィールド公開）を公開し、ルートの `lockerOf`/`syncOnceWrap`/`asCtx`/`scriptCoro` は委譲。`watchCtxDone`/`contextKey`/`scriptLocker` は内部のまま。生成テーブルは spec の `Dir` で `internal/stdlib/context/gen.go` に再生成。
- `text/template`/`html/template` → `internal/stdlib/texttemplate`,`internal/stdlib/htmltemplate`: `Install(bind, h, funcMap *runtime.TypeDef)`。`e.templateFuncMap()` は登録クロージャ内で呼ぶので、セッションごとの `FuncMap` typedef は保持される。`templateMust` は `stdlib.TemplateMust(name, target)` として共有化。`vm.RegisterFuncMapType` の `init()` は texttemplate パッケージへ移動。
- 間接の `time.Time`（WithDeadline 系の `a[1].(time.Time)` 判定）・source 優先（`e.bindBound` = 明示 Bind 優先）はそのまま。
- ベンチ（Intel Xeon, -benchtime=2s）: `BenchmarkNewSession` 9043→9466 ns/op、`BenchmarkSessionForkReload` PackageOnly 552µs→606µs / PackageAndCall 612µs→658µs。誤差範囲内（遅延化の費用は初回利用側に残っているので NewSession への影響はない）。
- lint の未使用検出で `namedSized`/`bytesSliceOf`/`runeSlice`/`h.fnvc`/`materializerOf` のルートラッパを削除（移動先が直接 `stdlib.*` を呼ぶため）。
- 注意: 移設時の機械置換で `"time.X"` などの文字列リテラル内までエイリアス名に書き換えてしまい `cannot use time.Duration as stdtime.Duration` の実行時エラーになった。引用符内は戻すこと。
