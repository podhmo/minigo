# minigo アーキテクチャ

minigo は Go ソースコードを**インタプリタ実行**するためのバイトコード VM です。
v1 (`minigo/`) が AST ツリーウォーカーだったのに対し、v2 は AST → バイトコード → スタックマシンという構成です。
設計の軸は **遅延性（laziness）** と **ホストへの埋め込みやすさ** の2つです。

- `go/packages` / `go/types` / `go list` は使わない（AGENTS.md の禁止事項でもある）。パッケージ解決は自前の `resolve` 層が行う。
- パッケージは「locate → parse → index」までを先に済ませ、宣言の**具体化（materialize）と初期化（init 実行）は需要駆動**。`p.Member("F")` で初めて `F` がコンパイルされ、import 先のパッケージは `OpGlobal`/`OpSelect` が触れた瞬間にロードされる。
- ホスト拡張は2系統: `Bind`（実 Go 値を import path に紐付け）と `RegisterSpecial`（呼び出しを AST quoted でハンドラに横取りさせる特殊フォーム）。

## パッケージ層

依存は一方向（下は上を知らない）:

```
cmd/minigo          CLI: run / repl / vet / gen-intrinsics
minigo (engine)    Engine, Bind/RegisterSpecial, installStdlib, Vet, WrapFunc, REPL
  ├─ host           ホスト向け stub パッケージ（panic("minigo intrinsic") 本体）
  ├─ vm             フレーム + 命令ディスパッチ + イテレータ駆動
  │   ├─ compile    AST → bytecode.Chunk（スコープを slot に解決）
  │   │   ├─ bytecode   Op 列（~100種）と Chunk{NLocals, NParams, Consts}
  │   │   └─ index      index.Build: 宣言だけ記録、評価しない
  │   └─ runtime    Value(=any), Cell, TypeDef, Package, Iterator, SpecialContext
  ├─ resolve        Resolver インターフェイス + GoScanResolver（ディレクトリ走査）
  └─ syntax         ParseFile → *File{AST, Imports}（import 表はファイルスコープ）
```

## 実行の流れ

```
minigo run ./app --entry F
    resolve.LocateDir("./app") → PackageMeta{Dir, GoFiles}
    syntax.ParseFile × N → []*syntax.File
    index.Build → p.Index（宣言の位置だけ、評価なし）
    p.EnsureReady()（LazyInit なら var/const 要求時まで遅延）
    materialize(pkg, decl) → *runtime.Function → vm.Call
        ... OpGlobal("pkg.Sym") → ImportRef.Materialize → 相手パッケージを
            まるごと同じパイプラインでロード
        ... OpSpecialCall → 登録済みハンドラに QuotedCall を渡す
```

重要なのは**エントリ以降は全部 need-driven** という点です。`import` 文はロードを引き起こさず、`pkg.Sym` を実際に触れる命令が走った瞬間に相手がロード・初期化されます。

## runtime 層の核

`runtime.Value` は `any` のエイリアスです。VM 内の具体的な値型:

| 型 | 意味 |
|---|---|
| `int64`/`float64`/`string`/`bool`/`Nil` | スカラー（ボックス化しない） |
| `*Cell` | 変数。`Elem` に値、`Typ` に宣言型タグ（再代入で coerce） |
| `*Struct`/`*Slice`/`*Map`/`*Chan` | 複合値。`Def`/`Typ` に宣言型タグ |
| `*Named{Typ, V}` | 宣言済み named basic のラッパー（`var c Celsius` のタグ） |
| `*TypeDef{Kind, Spec, Anon, Pkg, File, ...}` | 型。`Anon`/`Pkg`/`File` は要素型の遅延解決用 |
| `*Function`/`*Closure`/`*BoundMethod`/`*BuiltinFunc` | 呼び出し可能値 |
| `*GoValue{V}` | ホスト Go 値の箱（メソッドセットは reflect で見える） |
| `*TypedNil`/`*IfaceNil` | 型付き nil（`*Struct` の nil など）と interface nil |
| `*Iterator{Kind, ...}` | `range` 状態。Kind: `'s'` slice / `'m'` map / `'c'` chan / `'i'` int / `'r'` string / `'f'` func |
| `*Package`/`*ImportRef`/`*SymbolID` | パッケージ値、遅延 import、シンボル識別子 |

`Package` の状態遷移は `Empty → Indexed → Ready`。**`Index` は評価しない** — `index.Build` は宣言の位置と種別を記録するだけです。

**init タイミング**（`InitMode`）:

- `GoCompatibleInit`（既定）: `Member(name)` は**どのメンバでも**初アクセス時に `EnsureReady` — `func init() { populate(table) }` 型の副作用も `a.Lookup()` の呼出しだけで走る（main → helper → a の間接連鎖でも同じ）。var/const・type・func の種別は問わない。
- `LazyInit`（opt-in、ツール/quoting 向け）: FuncDecl/TypeDecl は init なしで materialize できる（var/const 要求時のみ `EnsureReady`）。`SymOf(a.F)` のような「解決だけしたい」用途はこちら。
- blank import（`import _ "a"`）は親パッケージの `__init__` 冒頭で `EnsureReady` — 推移的に連鎖する。

つまり「計算表を init で構築するパッケージ」は追加の登録機構なしで動きます。エントリポイントから直接 init を起動したい場合はホスト側から `e.Package(ctx, path)` → `p.EnsureReady()`。

## コンパイラ（compile/）

`compile` は AST を `*bytecode.Chunk` に落とす**トータル関数**です — 有効な Go 構文なら必ず出力を返し、未対応構文は `OpTrap`（実行時エラー）としてコンパイルする。名前解決はほぼ静的:

- ローカル/アップバリアブルは **slot 番号**に解決（`OpLocal A` / `OpUpval A`）。
- パッケージレベル名は `OpGlobal`（名前の const idx）で実行時解決 — import 表 → pkg env → builtins の順。
- 宣言型が見える場所（`var x T`、`func() T` の結果、パラメータ）には `typeExpr(t)` + `OpCoerce*` を emit して実行時の assignability チェックを埋め込む。
- `pkgAlias.Sym(...)` で import path が `e.specials` の `SymbolID` と一致すると `OpSpecialCall` に変換 — これが特殊フォーム。

## VM（vm/vm.go）

スタックマシン。`frame{fn, ch, locals[]*Cell, upvals, stack, ip, defers}` が1呼出。命令ディスパッチは `v.loop`（`for f.ip < len(code)`）です。

- **呼び出し**: `v.call` — BuiltinFunc は `Fn(v,args)`、TypeDef は convert、`*Function` 系は `prepFrame` → `v.exec`。`v.Call`（大文字）は境界で、スクリプトの `*Panic`/`*Trap` を Go エラーに戻す。
- **defer/recover**: `frame.defers` に登録、frame 解体時に `invokeDeferred`。BuiltinFunc の defer には sentinel frame を積んで `recover()` が検知できるようにしている。
- **ジャンプ**: `break`/`continue`/`goto`/`return` は `f.ip` の直接書き換え。`OpReturn` は `ip = len(code)`。
- **`f.boundLo/boundHi`**: range-over-func 用の実行区間制限（後述）。

## range-over-func（`iter.Seq`/`Seq2`）

コルーチンはありません。push モデルの生産者を pull モデルのループ内で駆動します:

- `newIterator` が `*Function`/`*Closure`/`*BoundMethod`/`*BuiltinFunc` を受けて `Iterator{Kind:'f', Fn}` を返す。
- 最初の `OpRangeNext` で `driveFuncIter` が生産者を **1回だけ** 呼ぶ。引数は `yield` BuiltinFunc。
- `yield` は引数をループ値として push → `f.ip = 本体先頭` → `runBounded`（`boundLo`/`boundHi` 内だけ `v.loop` を走らせる）で本体を実行。
- 本体が `top`（バックエッジ）に戻れば `yield` は `true`、それ以外（`break`/`goto`/`return`）では `false` + `it.Exited`。`false` の後に `yield` したら Go と同じパニック「range function continued iteration after yield returned false」。
- `OpRangeNext` は `f.ip` が `[top, end]` 内にある時だけ `end` にスナップ — ループ外へのジャンプ先を消さないため。

本体パニックは生産者フレームを普通に伝播するので defer/recover が Go 同様に働きます。

## 特殊フォーム（SpecialContext）

`RegisterSpecial(SymbolID{Path, Name}, handler)` で登録。コンパイラが呼び出し式を検知して `OpSpecialCall` にし、実行時はハンドラが `SpecialContext` を受けます:

| メソッド | 意味 |
|---|---|
| `ctx.Quote(expr)` | AST をソース文字列に戻す |
| `ctx.Eval(expr)` | 呼び出し側の環境で式を評価 |
| `ctx.Call(v, args)` | 値を呼び出す（関数なら実行） |
| `ctx.ResolveSymbol(expr)` | `pkg.Sym`/識別子を `SymbolID` に — **import 表だけ見るのでパッケージをロードしない** |
| `ctx.Resolve(expr)` | 値まで解決（locals → globals → import → `Member`、単一 decl のみ materialize） |
| `ctx.ResolveType(expr)` | 型式を `*TypeDef` に（`compile.typeExpr` と同じ形。`T[Args]` は instantiate） |

`QuotedCall` には呼び出し元の `Locals`/`Upvals`（名前→slot 写像）と `Call`（AST）が入るので、ハンドラは「呼び出し側のスコープを覗きながら評価」できる — convert-define の `define.Convert(...)` がこれで動きます。

## ホスト境界

- **`Bind(path, symbols)`**: 実パッケージを `map[string]runtime.Value` で登録（`State: Ready`、ソース不要）。stdlib の intrinsic（`fmt`/`strings`/`sort`/`os` 等）は `installStdlib` がここに流し込む。
- **`host` パッケージ（`minigo.dev/host`）**: stub 本体が `panic("minigo intrinsic")`。エディタからは普通の Go パッケージに見え、実行時は intrinsic テーブルが横取りする。`WithHostPolicy` でシンボル単位の公開を制御できる。
- **`WrapFunc(name, fn)`**: 実 Go 関数を `*BuiltinFunc` にする reflect アダプタ（marshal-by-copy、末尾 `error` は呼出エラー、複数戻り値は `*Tuple`）。引数は assignable/convertible ならそのまま、`[]T`/`map[K]V` パラメータには `[]any`/`map[any]any` から要素ごと変換する。`gen-intrinsics` が生成する `install.go` が `e.Bind` にこれを並べる。
- **`ValueOf(x)`**: ホスト値を runtime 値に変換する marshal ヘルパー。注意点: `runtime.Value` は `any` エイリアスなので `case runtime.Value:` は全マッチしてしまう — 具象型を列挙する必要がある。

## 型システムの近似

`go/types` は使わないので、型情報は実行時の `*TypeDef` とタグで近似します:

- `TypeDef.Kind`: `KindStruct`/`KindNamedBasic`/`KindSlice`/`KindMap`/`KindFunc`/`KindInterface`/`KindAlias`/`KindChan`/`KindPointer`。
- `coerce(x, td)` は `var x T = v` 相当の assignability を検査 — `*Named`/`Struct.Def`/コンテナの `Typ` を「宣言タグ」として見て、named-to-named の誤代入を「cannot use X as Y」で trap。
- 無名複合型の要素は `Anon`/`Pkg`/`File` で遅延解決（`compile.typeExpr` と同じ形を `ResolveType` が再現する）。
- ジェネリクスは `T[Args]` の明示インスタンス化 + 簡易推論（`inferBinds`）。制約 `~T`/union/comparable は概形の実装。

## ツール

| コマンド | 内容 |
|---|---|
| `minigo run <ref> [--entry F]` | パッケージの関数を実行（`minigo <ref> [F]` 短縮形） |
| `minigo repl` | 持続セッション REPL。`:reset`/`:exit`。未完了入力行は `IncompleteInput` で `.. ` 継続 |
| `minigo vet <ref> [--special path.Sym]...` | `panic("minigo intrinsic")` 本体を持つメンバへの未登録呼出を静的に報告（位置つき、検出時 exit 1） |
| `minigo gen-intrinsics -output <dir> <pkg>...` | `<path>/install.go` に `Bind` テーブル生成（関数→WrapFunc、var/const→ValueOf、型→合成 `*TypeDef`（KindNamedBasic＋仮想 Package）なので `var x pkg.T` でホスト値を型付き宣言できる。ジェネリックとメソッドはスキップ） |

## 既知の近似・限界（重要なもの）

- `go`/`select`/チャネルは単一スレッド近似（同期実行、無限バッファ、ブロックは trap）。真の goroutine 並行はスコープ外。
- `FindSymbolInPackage`（宣言1個だけの解決）は未実装 — `index.Build` がそもそも評価しないので差が小さい。
- スクリプト→ホストの marshal は値コピー（`*Struct` 等を直接書き換える intrinsic は自前で書く）。
- 名前解決は実行時 — 綴りミスのグローバルは実行時 trap（位置情報つき）。

## テストと開発

- `newEngine(t)` = `minigo.NewEngine("..")`、`run(t, e, "./testdata/<dir>", "Fn", args...)` が共通ハーネス。
- 機能テストは `testdata/features/main.go` に関数を追加して `TestFeatures` の表に載せる。
- 特殊フォームは `testdata/special` + `e.Bind("example.com/dsl", ...)` + `e.RegisterSpecial(...)` で検証。
- 変更後は `make format`（goimports）と `make test` が必須（AGENTS.md）。

設計の背景と各ラウンドの決定事項は `sketch/plan-minigo-vm.md`（英語、Round-N notes）を参照。
