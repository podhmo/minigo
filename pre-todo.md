# pre-todo

TODO.md に切り出す前の検討置き場。

- 出典: go-scan 援用調査 [docs/sketch/ja/exploration-goscan-reuse.md](./docs/sketch/ja/exploration-goscan-reuse.md) (PR #590)
- 一行一論点。項目ごと・行ごとに取捨できる粒度を目指す
- 実装方針はここでは未確定でよい。案の列挙まで
- 採用が決まったものだけ TODO.md の `- [ ]` 項目に昇格させる

## 強化(既存機能の純粋強化)

### syntax に軽量パース段階を追加する

- 現状
  - `syntax.ParseFile` は常にフルパース(`ParseComments` 付き)
  - import 一覧を取るだけのためにもフルパースのコストを払っている
- 参考実装
  - go-scan `scanner.ScanPackageFromFilePathImports` が `parser.ImportsOnly` を使う(調査 §3)
- 未定
  - `parser.ImportsOnly`/`PackageClauseOnly` を `syntax`/`resolve` のどこに置くか

### inspect に decl-anchored AST ハンドルを露出する

- 現状
  - `index.Decl` は `Func *ast.FuncDecl`/`Gen`/`Spec` を保持済み
  - body や初期化式は既にメモリ上にあるが、script 側からは見えない
- これがあると載せられる既存の TODO 項目
  - `inspect.Decls` が method を拾わない
  - free comment が見えない
  - const の initializer が見えない(`inspect.Value` が `init()` を走らせる問題の代替経路にも)
- 参考実装
  - go-scan `PackageInfo.AstFiles`/`FunctionInfo.AstDecl` が AST をそのまま保持(調査 §1)
- 未定
  - node ビューの形(`TypeExpr` 式の wrapper? `{Pos, Text, Children}` の最小形?)
  - 露出の粒度(decl 直下だけか、再帰的な node 木か)

### TypeExpr の解決経路を記録する

- 現状
  - `Origin`/`chaseType` は visited set だけを持つ
  - 循環・失敗時に「どこを辿ったか」を報告できない
- 参考実装
  - go-scan `FieldType.Resolve` が ctx に `ResolutionPathKey` で経路を持つ(調査 §2)
- 未定
  - 常時記録か、診断モードのときだけか

### Unresolved の明示的マーカー

- 現状
  - scope 外の参照は「見つからない」と「スキャン対象外」を区別できない
- 参考実装
  - go-scan `NewUnresolvedTypeInfo`/`TypeInfo.Unresolved` フラグ(調査 §2)
- 未定
  - `TypeExpr` に載せるか `Decl` に載せるか、両方か

### ファイルパースの並列化

- 現状
  - `minigo.go` がパッケージ内ファイルを逐次パースする
- 参考実装
  - go-scan `scanGoFiles` が errgroup + 並列度上限でパース(調査 §4)
- 未定
  - lazy スキャンでは対象ファイルが少ないので、効果が出る局面が限られるかもしれない

## 豊かさ(新しい機能面)

### astwalk パッケージ(仮称 — 旧 declwalk 案)

- 動機
  - func decl の body 走査は TODO.md 長年の残項目
  - inspect = 「何か」層(identity/解決)、walk = 「列挙」層と分けると設計が楽(調査 §1)
  - scanx 拡張は解釈側ソースなので host 機構を共有できず面倒
- 形の案
  - host 側 Go パッケージに本体、script 公開は薄い intrinsic(inspect の stub+impl 構成と同じ)
  - node ビューは `{Kind, Pos, Text, Children}` の最小形 + 型位置だけ TypeExpr
  - yield 型の `ast.Inspect` 相当(range-over-func が既に動く)
  - もしくは go-scan docgen 式の pattern+handler フック(呼び出しサイト照合)
- ついでにやると良いこと
  - `inspect.UsedSymbolsOf` の file walk をこちらに集約
  - file レベルの comment 列挙もここに載る
- 名前
  - astwalk を推す方向(「node」は広すぎ — 歩く対象は `go/ast` 構文木)
  - modulewalk(次項)と `-walk` で対称、`go/ast` 側に相当
  - 他候補: syntaxwalk(`syntax` pkg と語彙は揃うが拡張に見えて曖昧)、declwalk(decl-anchored に留まるなら正直だが狭い)、astx/astutil は x/tools と被るので不可
- 未定
  - bind path(`inspect` の増補か `minigo.dev/astwalk` 新設か)
  - 最小形だけか、pattern-hook まで入れるか

### modulewalk パッケージ(仮称 — ModuleWalker 相当)

- 動機
  - 「root 以下の全パッケージを nested go.mod 越しに列挙」が既存の TODO 項目
  - go-scan `modulewalker.go` がほぼそのままの仕様(調査 §3)
  - moon ide 的な表層走査ツールのキットとして使いたい意向あり
    - 3点セット: symbol index(場所を引く) + modulewalk(範囲を与える) + astwalk(中身を見る)
- 構成要素の案
  - imports-only スキャンで `PackageImports{Imports, FileImports}`
  - visitor 式 `Walk`(`./...` 展開つき BFS)
  - `FindImporters`/逆依存 map
  - `find-orphans` の `discoverModules`(go.work の `use`、nested go.mod)
  - `UnscannedGoFiles` 的な drift 検出
- レイヤリング
  - interpreter 層ではなく test-detect 的な tooling 層
  - 依存は imports-only 軽量パースのみ(AST 本体は要らない)= astwalk より下の層
  - astwalk との2分割は `go/build`(発見) vs `go/ast`(走査)と同じ標準的な境界
- 未定
  - 上の「軽量パース段階」が先に要る
  - script に公開するか、host ツール専用か

### ImportManager 相当

- 動機
  - 生成コードの import alias 解決を各 example が手でやるのは限界
- 参考実装
  - go-scan `importmanager.go`(keyword→`_pkg`、競合→連番、path ハッシュ fallback、`Qualify`)(調査 §4)
- 用途
  - gen-sync の managed import 領域
  - convert-define の生成コード
- 未定 → 方向は見えた
  - 「host util か script 公開か」の二択ではなく、host 側に実装を置いて script には薄い intrinsic を被せる(inspect の stub+impl 構成と同じ)
  - 純粋ロジック(alias 規則+衝突解決)で AST 不要なので共有しやすい

### scantest 型のテストハーネス + FileWriter

- 動機
  - examples の挙動確認テストが temp dir + 手書き helper 依存になりがち
    - 現に gen-sync は `setupModule`/`copyTree`/`assertSameFile` を自前で持つ
  - fuzz/difffuzz 系は対象外(差分オラクルで別物)
- 参考実装
  - go-scan `scantest.Run`(temp module→scan→action→assert)と `memoryFileWriter`(調査 §4)
- 対象
  - 生成物を出す example の挙動確認テスト(gen-sync、convert-define)
- 未定
  - どの example から適用するか

### 永続シンボル index(symbolCache 相当)

- 動機
  - 「シンボル X がどこで定義されているか」を引く index がない
  - REPL からの package introspection(既存 TODO)の足場になる
- 参考実装
  - go-scan `cache.go` の `symbolCache`(調査 §4)
  - 中身は JSON 1ファイル。`symbols{pkgpath.Name → relpath}` + `files{relpath → {symbols}}` の2マップ
  - 訂正: 検証は mtime ではなく「ファイルの存在/新規/削除」ベース(mtime は go-scan 側で外された)
- 必要な局面
  - REPL/反復ツール時のみ。一回きりの実行ではセッション内 index が足りる
- コスト
  - 全ファイル前処理ではなくスキャン済み分だけ逐次蓄積。検索は map lookup + `os.Stat` 1回でほぼゼロ
- 経路デバッグとの関係
  - 「どのアクセスで次パッケージを読んだか」は load 時の slog(pkg 単位+caller)で足りる
  - 全シンボルの解決履歴は重すぎる — 失敗チェーン1本だけ持つ `ResolutionPathKey` 型が限度
  - AST は現状ヒープから解放されない(`e.pkgs`/`e.byDir` に evict なし)ので token.Pos+保持 AST で復元可能
- 未定
  - 永続化の置き場所と粒度

## 既存 TODO 項目との対応

昇格の際に既存項目へマージまたは参照を付けるための対応表。

- const `inspect.Value` が `init()` を走る → go-scan `ConstantInfo.ValExpr`/`evalConstExpr` が参照先(§4)
- func body 走査 → declwalk 項目が本体(§1)
- `List[int]` の instantiate 表示 → `FieldType.TypeArgs` の表現(§4)
- canonical package 名の first-sorted-file 問題 → dominant-name 二段階。修正箇所は `resolve.ReadPackageFiles`(§4)
- repo-enumeration surface → `ModuleWalker`/`discoverModules` が仕様(§3)
- REPL package introspection → `symbolCache`/`FindSymbolDefinitionLocation`(§4)
- `inspect.Decls` が method を拾わない → `PackageInfo.Functions` がモデル(§4)
- free comments 不可視 → `AstFiles` 保持がモデル(§4)
