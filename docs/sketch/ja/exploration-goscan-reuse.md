# go-scan パッケージ群の援用調査

依頼: `inspect` パッケージ(とくに TODO.md の func decl body 走査)と
`examples/gen-sync` のような「パッケージとファイルを渡り歩く操作」に、
`podhmo/go-scan` のパッケージ群を援用できないか。
「援用」にはそのまま使うだけでなく、設計の写経・仕様の借用を含める。

対象外: go-scan 内の `minigo/` `minigo2/` `symgo/`(巨大で、後続で取り込む予定の
パッケージ)。それらを使う example(docgen 等)が示す設計パターンだけを
参考にする。

調査対象: go-scan@`c659f32` / minigo@`899ac68`。

## 前提として確認したこと

- minigo には既に go-scan の vendored copy がある: `pkg/locator/` ←
  `locator/`(`pkg/SOURCE.md` に出所 commit と再同期手順を記録済み)。
  「ファイルをコピーして適合させる」経路は確立済みで、go.mod 依存には
  していない。
- minigo の `index.Decl` は `Func *ast.FuncDecl` / `Gen *ast.GenDecl` /
  `Spec ast.Spec` / `Inherited`/`InheritedType` を保持している。func の
  body も const/var の初期化式も、AST としては既にメモリ上にある。
  inspect が公開していないだけ。
- `inspect.UsedSymbolsOf` は既に `ast.Inspect(f.AST, ...)` でファイル全体
  (func body を含む) を走査している。「AST を舐める」コード経路は
  inspect 内に既にある。
- go-scan 側も minigo 側と同じ制約で書かれている(go/packages, go/types,
  `go list`, testify 禁止; go-cmp, slog 強制)。そのまま移植しても
  ルールに抵触しない。
- 構造差: go-scan のモデルは host ツール向けの解析結果モデル、minigo の
  inspect はインタプリタ内スクリプトに見せる FFI ビュー。直接 import は
  筋が悪く、「設計の写経」(script 側)と「vendored copy」(host 側)が
  現実的な援用形になる。

## 対応関係の地図

| minigo 側 | go-scan 側 | 備考 |
| --- | --- | --- |
| `syntax.File` (*ast.File + import 表) | `scanner.PackageInfo.AstFiles` + `Fset` | go-scan は `parser.ImportsOnly` の軽量経路も持つ |
| `index.Index` / `index.Decl` | `scanner.PackageInfo{Types,Constants,Variables,Functions}` / `TypeInfo` / `FunctionInfo` / `ConstantInfo` | `Functions` はメソッド込み(`Receiver` で区別) |
| `inspect.TypeExpr` (ast.Expr のハンドル) | `scanner.FieldType` | FieldType は `TypeArgs`/`IsTypeParam`/lazy `Resolve` を持つ |
| `inspect.Resolve`/`Origin` の Resolver | `scanner.PackageResolver` iface + `FieldType.Resolve` | go-scan は解決パスを ctx で伝播する |
| `pkg/locator` + `resolve.Resolver` | `locator` + `goscan.Scanner` | locator は vendored 済み |
| scanx `Explorer`/`Reach` | `Scanner.ScanPackageFromImportPath` + `FieldType.Resolve` + convert の `collectFields` | lazy な cross-package 型走査 |
| scanx `TypeRefs` | `FieldType` のツリー | named leaf 収集 |
| (なし) | `ModuleWalker` + `PackageImports` | imports-only scan + walk + 逆依存 |
| (なし) | `ImportManager` | codegen 用 import alias 解決 |
| (なし) | `symbolCache` / `ListExportedSymbols` / `FindSymbolDefinitionLocation` | mtime 検証つき永続シンボル索引 |
| (なし) | `scantest` + `writer.FileWriter` | temp module + in-memory 出力キャプチャ |
| `inspect.Implementers`/`MethodSet` | `type_relation.go` (`Implements`, `getAllInterfaceMethods`, `findMethodInfoRecursive`) | 仕様のオラクルとして読める |
| `inspect.EnumMembers` | `scanner.resolveEnums` | 同じ意味論 |

## 個別項目の調査結果

### 1. func decl の body 走査

TODO 項目「Script-side traversal of function bodies」(と背景の
`grafana-openapi`: `registerRoutes` の body 内に route 登録がある件)。

go-scan の答えは 3 層に分かれている:

- **AST の保持**: `PackageInfo.AstFiles` と `FunctionInfo.AstDecl`
  (`*ast.FuncDecl`) は body ごと保持される。body を捨てるのは
  `WithDeclarationsOnlyPackages` 指定時だけ(`scanGoFiles` 内で明示的に
  `f.Body = nil` を入れる省メモリモード)。つまり「body がある」が
  デフォルト。
- **軽い body walk の実例**: `scanner/scanner.go` の `parseFuncDecl` は
  `ast.Inspect(f.Body, ...)` で関数内の local type decl を拾い、local
  alias の即時解決まで行う。「body 全体をモデル化せず、目的のノード種
  だけ拾う」実践例。
- **重い用途は symgo へ**: route 登録収集(docgen)、call graph
  (goinspect)、使用マーク(find-orphans)はすべて symgo の評価に
  逃がしている。go-scan 自身も「汎用の body モデル」は提供しておらず、
  深さに応じて軽い `ast.Inspect` とシンボリック実行を使い分けている。

もうひとつ、body 走査の「形」として docgen が示しているのは
**パターン + ハンドラ**である(`examples/docgen/patterns`):
`Pattern` は callee のプレフィックスマッチと `Apply` ハンドラの組で、
ユーザ定義のパターンは Go 製の設定ファイル(minigo2 で評価)から読む。
`registerRoutes` 内の `HandleFunc` 系呼び出しを拾いたいだけなら、
生の stmt/expr ストリームを全公開するよりこの形の方が小さく済む。

minigo への示唆:

- `index.Decl.Func` に body AST は既にある。「decl 起点の ast.Node
  ハンドル + file context」を inspect の TypeExpr と同じ方式で包めば、
  最小構成の body ビューは薄く作れる。
- range-over-func は既に動くので、`ast.Inspect` 相当を yield 型の
  iterator として script に出せば、フィルタはスクリプト側に書かせられる。
  これは API 表面が 1 個のイテレータで済む最小案。
- 「全 stmt/expr の子供モデル」は go-scan も持っていない。pattern-hook
  型で始めて、必要になったら深くするのが両リポジトリの実践に沿う。

参照: `scanner/scanner.go` (`parseFuncDecl`, `isDeclarationsOnly`),
`examples/docgen/patterns/patterns.go`, `examples/docgen/loader.go`,
`examples/docgen/analyzer.go`。

### 2. パッケージ・ファイル横断(gen-sync の scanx)

gen-sync がスクリプト側で実装しているもの:

- `Explorer`: scope-gated lazy package load + per-package decl cache +
  否定キャッシュ
- `TypeRefs`: `TypeExpr.Children` を辿って named leaf を収集
- `Reach`: 型参照の BFS(visited set による停止)

go-scan の対応物は host 側に揃っている:

- `scanner.FieldType` が lazy 解決ハンドル: `Resolver PackageResolver`
  iface + `Definition` キャッシュ + `ResolutionPathKey` による循環検出。
  `FieldType.Resolve` が「必要になった時点で他パッケージを scan する」
  lazy の実装例で、Explorer とほぼ同じ意味論。
- `Scanner.ScanPackageFromImportPath` はその Resolver 実装。
- `examples/convert/parser` の `collectFields` は Reach と同じ
  cross-package 型走査。`resolveType` は doc 内のアノテーション文字列から
  `FieldType{Resolver: s, FullImportPath, TypeName}` を手組みして解決
  する——「名前から resolvable handle を組み立てる」パターン。

差分として注目したい点:

- `FieldType.Resolve` は ctx に解決パスを伝播させ、循環検出と
  `--inspect` ログの両方に使う。minigo の `TypeExpr.Origin`/`chaseType`
  は visited map 止まりで、「どこを経由して解決したか」をエラーや
  ログに出せない。写経する価値がある。
- `TypeInfo.Unresolved` + `NewUnresolvedTypeInfo` は「スキャン対象外」を
  表す明示的 marker。`Explorer.Lookup` は nil を返すだけで「範囲外か
  未発見か」を区別できない。TypeExpr/Decl に相当の kind を持つのは
  正直な設計。
- `deps-walk` は `ModuleWalker` の `Visitor` パターンを使う:
  `Visit(pkgImports) ([]string, error)` が次のキューを返すので、hop
  制限・正/逆方向・`-ignore`/`-hide`・file granularity のような
  進行制御を walk 本体から切り離せる。`Reach` の固定 BFS より汎用的。

### 3. repo 列挙・逆依存(TODO: repo-enumeration surface / REPL introspection)

「ルート以下の全パッケージ(ネストした go.mod を跨いで)を test ファイル
の有無とともに列挙する」という要求に、ほぼそのままの部品がある:

- `modulewalker.go` の `ModuleWalker`:
  - `ScanPackageFromFilePathImports` — `parser.ImportsOnly` の軽量
    parse + dominant package 名 + パッケージ単位キャッシュ。
    `PackageImports{Imports, FileImports}` でファイル粒度の import も
    取れる(test-detect の per-file import と同じ情報)。
  - `Walk` — visitor パターンの BFS、`resolvePatternsToImportPaths` で
    `./...` を展開。
  - `FindImporters` / `FindImportersAggressively` — dir 全走査の逆引き。
  - `BuildReverseDependencyMap` — 逆依存マップ。
- `tools/find-orphans` の `discoverModules` — `go.work` の `use` を
  `x/mod/modfile.ParseWork` で読み、無ければ go.mod 列挙 walk
  (vendor とドット dir を除外)。「go.mod を跨いだ列挙」そのもの。
- `examples/deps-walk` は上記を組み立てた end-to-end の実用例
  (DOT/Mermaid/JSON、hop 制限、`-test` で _test.go 込み)。

minigo 側の穴:

- `pkg/locator` は「1 module 内で named import を dir に解決」するだけ。
  モジュール列挙も walk もない。
- `syntax.ParseFile` は常にフルパース(`ParseComments`)。
  `parser.ImportsOnly` 相当の軽量経路がないので、imports-only の列挙は
  現状では重い。`resolve.ReadPackageFiles` は `PackageClauseOnly` で
  名前だけ読んでいるので、「package clause」「imports」「full」の
  3 段階を揃えるのが自然。
- `ReadPackageFiles` は `_test.go` を常に除外する。test-detect 系の
  用途には `WithIncludeTests` 相当のスイッチが要る。

### 4. 小粒の一致項目(仕様・実装の借用ネタ)

- **canonical package name の多数決化(TODO)**: go-scan は dominant
  package name を 2 pass で決める(`_test` 許容、`main` は他に非 main が
  あれば降格、複数の非 test 名 = エラー)。minigo の
  `resolve.ReadPackageFiles` は最初にマッチした 1 ファイルの clause
  だけ読む → gen-sync が抱える「first sorted file wins」問題への
  直接の答え(`scanner/scanner.go` の `scanGoFiles` と
  `ScanPackageFromFilePathImports`)。
- **const の initializer と値(TODO: `inspect.Value` が init を走らせる /
  cell ポインタが見える)**: `ConstantInfo.ValExpr` は初期化式を AST の
  まま保持し、`ConstVal`/`IotaValue`/`RawValue` は `evalConstExpr`
  (go/constant による再帰評価。iota、ident 依存、循環検出あり、init
  は走らない)で計算する。inspect には配列長用の `constInt` という
  部分版が既にある。`math/bits.UintSize=64` のような arch 定数の
  特例ハックは、同種の逃げ方をするときの前例。
- **free comments / Decls に methods が入らない(TODO)**: go-scan は
  `PackageInfo.AstFiles` を保持するので `astFile.Comments` で free
  comment が読める(`examples/convert/parser` が実際に
  `// convert:import` をそこから拾っている)。`Functions` は
  メソッド込みで、`TypeInfo.Node`/`FunctionInfo.AstDecl` で decl node に
  逆引きできる。minigo でも `index.Decl` は `Func`/`Gen`/`Spec` を
  保持しているので、engine-only accessor を inspect 側に 1 本足す形で
  両方塞がる。
- **generic instantiation(TODO: convert-define の `List[int]`)**:
  `FieldType.TypeArgs` + `IsTypeParam`/`IsConstraint` + decl 側の
  `TypeParamInfo`。minigo の TypeExpr は `Children` で base→args を
  出すだけ。instantiation を「named node + args」の組で表す設計は
  写経候補。type-argument substitution 自体は go-scan も行っていない
  (symgo 側)ので、ここは「表現」だけ借りる項目。
- **`importmanager.go` の `ImportManager`**: codegen 時の import
  alias 衝突解決(キーワード `_pkg`、数字サフィックス、sanitize)+
  `Qualify`。gen-sync の managed import region や convert-define の
  生成コードで「安全な局所名」を決める部品として、vendored-copy 候補。
  依存は `scanner.PackageInfo` だけなので切り離しやすい。
- **`writer.go` + `scantest`**: `FileWriter` interface 越しの出力で
  生成物を in-memory にキャプチャする(`memoryFileWriter`)。
  `scantest.Run` は「temp module 組み立て → scan → action → 出力 map
  を検査」の定型。gen-sync は現状 temp dir に実 write しているので、
  examples のテスト作法の参考になる。
- **`cache.go` の `symbolCache`**: mtime 検証つき symbol→file の永続
  キャッシュ(`SaveSymbolCache`/`FindSymbolDefinitionLocation`/
  `ListExportedSymbols`/`getFilesToScan`)。REPL introspection や
  「repo 内のシンボル X はどこ」に使える、invalidation まで実装済みの
  前例。
- **並行 parse**: `scanGoFiles` は `errgroup` でファイルを並行に
  パースする(`WithParallelismLimit` で上限)。grafana 規模の lazy scan
  で効く速度系。minigo は直列パース。
- **`Overlay` / `ExternalTypeOverride`**: in-memory ファイル差し替えと
  合成 TypeInfo。Overlay は locator と一緒に移植済み。override は
  minigo の bound/host decl(`NewHostDecl`/`NewHostType`)に相当——
  ここは設計が既に揃っている。
- **`UnscannedGoFiles`**: ディレクトリの `.go` と parse 済み集合の差分。
  「取りこぼし検出」に転用できる。

### 5. 対象外領域が示す設計情報(利用側だけ)

symgo/minigo2/minigo 自体は対象外だが、それらを使う example の構造は
分離のしかたとして参考になる:

- docgen: host が analyzer + intrinsic registry を持ち、マッチした
  呼び出しで `Apply` がモデルを組み立てる。「解析を hook として
  登録する」形は body 走査の完成形のひとつ。
- `docgen/loader.go`: minigo2 で `Patterns` var を読む DSL 設定ファイル。
  「minigo スクリプトを設定言語にする」先例で、minigo2 取り込み後に
  そのまま参照できる。
- find-orphans: `Walker.Walk` で import 網羅 → symgo が使用マーク。
  「列挙層と解析層の分離」の実例。

## 持ち込み方について

- go-scan を go.mod 依存にするのは非現実的: minigo2/symgo という
  別のインタプリタ実装を丸ごと引きずる上、flagstruct/orderedmap 等の
  依存が増える。実績のある経路は `pkg/locator` の vendored copy で、
  `pkg/SOURCE.md` に出所と再同期手順を記録する形。
- host 側の部品(`locator` 追加機能、`ImportManager`、`ModuleWalker`
  相当、`scantest` 相当)はコピーして適合させる。go-scan 側も
  go/types 不使用なので移植は素直。
- script 側(`inspect`)には「設計の写経」が主な援用形になる:
  `FieldType.Resolve` の resolution path、`Unresolved` marker、
  `AstFiles`/`Node` 保持による自由到達、imports-only の軽量段階、
  dominant package 名。

## まとめ(優先順位の提案)

1. `syntax` に `ImportsOnly`/`PackageClauseOnly` 相当の軽量パース段階 +
   `ModuleWalker` 相当の walk(列挙 + imports 収集)を用意する。
   repo-enumeration surface と REPL introspection の TODO を直接前進
   させる。実装参照: `modulewalker.go` + `examples/deps-walk` +
   `find-orphans` の `discoverModules`。
2. dominant package name を `resolve.ReadPackageFiles` に移植(数十行)。
   gen-sync の「first sorted file wins」のTODOを解決。参照:
   `scanner/scanner.go` の 2 pass。
3. `inspect` に decl-anchored の AST handle(`Decl` の `Func`/`Spec`/
   `Gen` を包むもの)を足す。`inspect.Decls` が methods を見落とす件、
   free comments、const initializer の三件の前提になる。
   `PackageInfo.AstFiles`/`Node` 保持が設計参照。
4. body 走査は「pattern-hook」型で小さく始める: `ast.Inspect` 相当の
   yield walk か、docgen の `Pattern` 型の「callee → handler」フック。
   全 stmt のモデル化は go-scan もしていないので後でよい。
5. `FieldType` の `TypeArgs`/`IsTypeParam` 表現、`Resolve` の
   resolution-path 記録、`Unresolved` marker は `TypeExpr` 系の次の
   改修の設計参照にする(generic instantiation 対応と合わせる)。
6. `ImportManager` / `scantest` / `FileWriter` は codegen 系 examples
   の次の改修時に vendored-copy / 写経する。
