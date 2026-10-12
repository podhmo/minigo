# minigo エンジン利用ガイド（ホスト側 Go API）

`minigo` パッケージ（リポジトリルート）が Go 側からスクリプトを動かすための API。`inspect` などのスクリプト向け intrinsic の案内は [inspect-api.md](inspect-api.md) を参照。このドキュメントは「エンジンの作り方・読み込み方・呼び出し方」の使い分けをまとめたもの。

## エンジンの構築

```go
e := minigo.NewEngine(startDir, opts...)
```

`startDir` は resolver の起点ディレクトリ（カレントワーキングディレクトリ相当）。Option はすべて `With*` 形式。

| Option | 効果 | 使う場面 |
|---|---|---|
| `WithBuildConfig(cfg)` | GOOS/GOARCH/build tags を指定 | クロスコンパイル相当のファイル選択 |
| `WithInitMode(m)` | パッケージ初期化のタイミング | `LazyInit` = var/const/init の副作用を走らせず名前・型だけ欲しいツール向け（default は `GoCompatibleInit`） |
| `WithPackageModes(map)` | import path ごとの応答方式 | `ModeSource` = bound 登録があってもソース解釈を強制、`ModeDeny` = import を明確に失敗させる、未指定は `ModeAuto`（bound 優先→source フォールバック） |
| `WithAllowedRoots(roots...)` | resolver が返してよいディレクトリの制限 | サンドボックス化 |
| `WithHostPolicy(allow)` | bound ホストシンボルの可視性フィルタ | スクリプトに見せたくない stdlib 機能の遮断 |
| `WithOutput(w)` | print/println/fmt.Print 系の出力先 | |
| `WithArgs(argv)` | `os.Args` 相当 | |
| `WithWorkingDir(dir)` | 作業ディレクトリ上書き | |

`WithPackageModes` が「bound と source の境界をオプションで切る」機能（`ModeSource`/`ModeDeny`）。inspect 側にも `BoundOf`/`SourceOf` として露出済み。

## パッケージの読み込み（ref の3形態と境界）

`e.Package(ctx, ref)` は ref の形で自動 dispatch する。

| ref の形 | 行き先 | 境界 |
|---|---|---|
| import path (`"strings"`, `"github.com/x/y"`) | `loadPath` — **bound 登録があればそれ優先**、なければソース解釈 | bound 側は index を持たない（inspect で `Kind "host"` 擬似 decl） |
| dir (`"./app"`, `"/abs/dir"`, `"."`) | `loadDir` → `LocateDir` | **実パッケージ**: sibling `.go` 全部、build constraints（`//go:build`・`_GOOS` サフィックス）適用、`byDir` キャッシュ |
| `.go` ファイル (`"main.go"`) | `loadDir` → `LocateDir` → `locateFile` | **単一ファイルパッケージ**: sibling は読まない、build constraint は無視（`//go:build ignore` の DSL ファイルが動く）。import path は合成名 `"<file>"+abs` で他から import できない |

つまり「dir を渡すか file を渡すか」で**同じディレクトリでも中身が変わる** — `"./app"` ならパッケージ全体（constraint で除外されるファイルは見えない）、`"./app/main.go"` ならそのファイルだけ（constraint 無視）。

### 中間形と補助ローダー

| 関数 | 何を返すか | 使い分け |
|---|---|---|
| `LoadFile(ctx, file)` | 単一ファイルパッケージ（`loadDir` の `.go` 経路と等価だが engine 側キャッシュ `files` 管理） | `RunFile` の中身 |
| `LoadDirScope(ctx, file)` | 指定ファイル + **同一 package 名の sibling** で作る dir-scope パッケージ | build constraint フィルタ**なし**（`//go:build task` 系の別ファイルも見える）。キャッシュされない（毎回 index し直し） |
| `SourceOf(ctx, path)` | bound シャドウを迂回してソースだけ index。bound でなければ `Package` と同じ | bound pkg が index なしで詰まるとき。private cache（`srcs`）なので実 import の応答を侵さない |
| `BoundOf(path)` | bound 登録パッケージのみ（なければ nil） | bound 判定 |
| `BoundPackages()` / `BoundSymbols(path)` | bound 一覧 / その path のシンボル名 | デバッグ・ポリシー確認 |

lenient 版（`loadDirLenient`/`loadFileLenient`）は `inspect.DirOf`/`FileOf` の裏側専用 — redeclare エラーを緩めて壊れたソースも index するので、strict 側のキャッシュと混ざらないよう別キャッシュに入る。

## 呼び出し方（Run/Call/EvalExpr）

| 関数 | 入力 | 使い分け |
|---|---|---|
| `Run(ctx, ref, fnName, args...)` | ref（上記3形態どれでも）+ 関数名。`fnName == ""` は `main` | **ワンショット実行**。mainPkg を記録して `runtime/debug.ReadBuildInfo` が答えられる |
| `RunFile(ctx, file, fnName, args...)` | `.go` ファイル | `LoadFile`+`Call`。ref dispatch を通さない明示の単一ファイル実行 |
| `RunResult(ctx, ref, fnName, args...)` | 同 `Run` | 戻り値を `Result` で包んで `r.As(&dst)` でホスト側の型に取り出す（reflect 代入） |
| `Call(ctx, pkg, name, args...)` | ロード済み `*runtime.Package` + メンバ名 | **1 Call = 1 process = 1 根 VM**。呼び出し中に spawn された goroutine は全部この process scope に join し、return で畳まれる。引数は snapshot として境界を渡る（`[]string`→`*runtime.Slice` 等、ホスト値への書き戻しは起きない）。戻り値は `runtime.Unwrap` 済み |
| `EvalExpr(ctx, pkg, file, expr)` | 生 AST 式 | REPL が式評価に使う低レイヤー入口 |

`Call` の使いどころは「同じパッケージから複数メンバを順に呼ぶ」「ロードと実行を分けたい」ケース。`Run` 系は ref を毎回解決する convenience。

## spawn する/しない（`NewSession`）

```go
sess := e.NewSession()
```

**セッション = 独立した Engine**。何が新しく何が引き継がれるか:

- 新規: パッケージキャッシュ（`pkgs`/`byDir`/`files`/lenient 系全部）、FileSet、`mainPkg`
- 継承: resolver、build config、`specials`、hostPolicy、pkgModes、出力先、cwd、args、**bound パッケージ**（カスタム `Bind` も intrinsics も、session 側で同名登録があれば session 優先）

使い分け:

- **spawn する**: スクリプト実行でエンジン状態（index 済みパッケージ、`mainPkg`）を汚したくないとき。REPL は1セッションを切って逐次入力を走らせる。複数タスクを直列に走らせるが互いのロード履歴を見せたくない場合も。
- **spawn しない**: 同じコンテキストで連続して使う通常ケース（`Run`/`Call` をそのまま連打）。`Call` 自体は毎回 VM を立てるので、**実行の分離は `Call` が既に担保**している — `NewSession` は実行ではなく**ロード状態**の分離。

紛らわしい点: `Call` の中でスクリプトが `go f()` した goroutine は「spawn」だが、これは VM 内 process scope の話で `NewSession` とは別レイヤー。`Call` は return 時に配下 goroutine を畳む。

## ハマりどころ

- **`Package("./app/main.go")` で sibling が見えない** — `.go` 指定は単一ファイルパッケージ。sibling 込みなら `"./app"`（dir）か `LoadDirScope`。
- **`//go:build ignore` のファイルが `Package("./dir")` で見えない** — dir 経路は build constraint を適用する。意図的に見せたいなら `.go` 指定（`LoadFile`）か `LoadDirScope`。
- **bound パッケージを inspect すると index が nil** — `Package`/`PackageOf` は bound 優先。ソースが欲しければ `SourceOf`（エンジン側・スクリプト側同名）、恒久的にソース解釈させるなら `WithPackageModes(ModeSource)`。
- **init の副作用を走らせたくない**（型名の列挙だけしたい等）— `WithInitMode(LazyInit)`。`GoCompatibleInit`（default）は最初のメンバアクセスで init が走る。
- **引数の型が合わない・戻り値の型が runtime 系** — `Call` は snapshot/`Unwrap` 済み。`RunResult.As` もある。手動で unwrap するなら `runtime.DeepUnwrap`/`runtime.ErrorOf`（#877）。
- **`mainPkg` が立たない** — `Run`/`RunFile` 経由だけが mainPkg を記録する。`Call` 直接呼び出しでは `runtime/debug` の build info は空。

## 関連

- `minigo.go` — `Engine`/`Option`/`Run`/`Call`/`NewSession` の実体（各関数の doc コメントが契約の正本）
- `resolve/` — `Resolver`/`LocateDir`/`LooksLikeDir`/`FileImportPrefix` の ref→パッケージ解決
- `docs/ja/inspect-api.md` — スクリプト側 `minigo.dev/inspect` の使い分け
- `docs/ja/minigo-architecture.md` — Engine が載るパッケージ層の全体像
