# pre-todo

TODO.md に切り出す前の検討置き場。

- 出典: go-scan 援用調査 [exploration-goscan-reuse.md](./exploration-goscan-reuse.md) (PR #590)
- 一行一論点。項目ごと・行ごとに取捨できる粒度を目指す
- 実装方針はここでは未確定でよい。案の列挙まで
- 採用が決まったものだけ TODO.md の `- [ ]` 項目に昇格させる
- 各項目に **処遇** マークを付けている(昇格済み/先送り/見送り/未昇格) — 実装後に TODO.md → TODO-archive.md へ流れても判断記録はここに残る

## 強化(既存機能の純粋強化)

### syntax に軽量パース段階を追加する

- 処遇: **昇格済み**(TODO.md — `ParseImports`/`ParsePackageClause`)
- 現状
  - `syntax.ParseFile` は常にフルパース(`ParseComments` 付き)
  - import 一覧を取るだけのためにもフルパースのコストを払っている
- 参考実装
  - go-scan `scanner.ScanPackageFromFilePathImports` が `parser.ImportsOnly` を使う(調査 §3)
- 未定 → 方向は見えた
  - `ParseFile` にモードオプションは足さない — truncated `*ast.File` が同じ型で下流に流れ、フル AST 前提の処理が静かに壊れるため
  - 別関数・別戻り値型にする(go-scan `ScanPackageFromFilePathImports` と同じ切り分け)
    - 命名: `syntax.ParseImports` に**決定** — 既存 `ParseFile` ファミリに揃える
    - `ParsePackageClause` も同じファミリで spec 実在用語;import 側は spec 的には `ImportDecl`/`ImportSpec` で「ImportsClause」は存在しないので `ParseImports` が自然
    - truncated AST はその関数内で消費・破棄されるので half-AST がパイプラインに存在しない

### inspect に decl-anchored AST ハンドルを露出する

- 処遇: **昇格済み**(TODO.md — raw AST 素通し)
- 現状
  - `index.Decl` は `Func *ast.FuncDecl`/`Gen`/`Spec` を保持済み
  - body や初期化式は既にメモリ上にあるが、script 側からは見えない
- これがあると載せられる既存の TODO 項目
  - `inspect.Decls` が method を拾わない → **欲しい(verdict)**、ただし欠けているのは pkg-wide flatten のみ
    - 訂正: `inspect.MethodsOf(typeDecl)` は既に存在し script にも bound 済み — per-type は既に取れる
    - パッケージ全体は `Decls`→TypeDecl 絞り→`MethodsOf` の ~5 行ループで届く — ただし組み合わせを知らないと使えない(発見可能性がない)ので、薄い pkg-level 列挙(`inspect.Methods(pkg)` 相当)を置く意味はある
  - free comment が見えない
  - const の initializer が見えない(`inspect.Value` が `init()` を走らせる問題の代替経路にも)
- 参考実装
  - go-scan `PackageInfo.AstFiles`/`FunctionInfo.AstDecl` が AST をそのまま保持(調査 §1)
- 未定 → 方向は見えた
  - node ビューの形: 初版はラップなし素通し — `inspect.ASTOf(decl)` 相当で生の ast ノードを返す
    - script 側は host 値の reflect facade 経由で `.Name`/`.Body`/`.Pos()` 等に触れられる(遅いが十分)
    - `{Kind,Pos,Text,Children}` ビューは「うるさすぎたら」or「重たかったら」後で考える(consumer-first)
    - 「遅いか」の判定は推測でなく実測で — ただし一回きりの特性確認で足りる(repo のテストに `testing.B` 機構は増やさない)
      - 実用的な example(swagger:route 的な method 全数走査 / gen-sync 的な生成系)を大きな対象に向けて走らせ、host Go 版と wall time 比較 — O(N) 操作なので数字が出れば判断できる
      - 大きな対象は minigo-usecasefuzz realworld/ がそのまま使える → 性能測定は usecasefuzz 行き
  - 露出の粒度(decl 直下だけか、再帰的な node 木か)は素通しなら問い自体が消える — 生 AST なら木全体がそのまま触れる

### TypeExpr の解決経路を記録する

- 処遇: **昇格済み**(TODO.md — 常時記録・call-local)
- 現状
  - `Origin`/`chaseType` は visited set だけを持つ
  - 循環・失敗時に「どこを辿ったか」を報告できない
- 参考実装
  - go-scan `FieldType.Resolve` が ctx に `ResolutionPathKey` で経路を持つ(調査 §2)
- 未定 → 方向は見えた
  - 常時記録で十分 — コストは ctx put 1回 + ホップ毎のスライス append のみ(ホップは実用上数十以下)
  - 経路は call-local の一時状態でシンボル側には残さない — 解決呼び出しの性質であってシンボルの性質ではない
    - `chaseType` が既に visited set を call 毎に持つので並行して []string を1本伸ばすだけ
    - 診断モードという概念は要らない — 成功時は読まれず失敗/循環時のみエラーメッセージに使う

### Unresolved の明示的マーカー

- 処遇: **先送り** — TODO.md には入れない(発火条件=解釈停止機構が main に入るまで)
- 現状
  - scope 外の参照は「見つからない」と「スキャン対象外」を区別できない
- 参考実装
  - go-scan `NewUnresolvedTypeInfo`/`TypeInfo.Unresolved` フラグ(調査 §2)
  - 実体は symgo の **load policy 境界マーカー**: `Resolver.ResolveType` が `ScanPolicy(importPath)` NO で placeholder を返す + 解決失敗時にも逃げる
- 未定 → 先送り(発火条件つき)
  - 層: interpreter 層ではない(VM は実値が要るので placeholder では進められない)。inspect/tooling 層の型解決が作用面
  - 価値は「読み込み範囲を制限する policy」が存在して初めて立つ — policy 導入の前提部品
    - 現状 policy 系の機能は draft 実験 PR 側にのみ存在(#24 `inspect.Ops` dataflow、#25 compiled ops body 走査、#112 trace 系)、main には無い
  - 発火条件: 途中で解釈をやめる機構の例が main に入ってから — そこで必須パーツとして復活
  - 載せる場所: **両方(verdict)** — 式の中でシンボルを返せないと意味がないので TypeExpr 側は必須、Decl 側は decl 面の報告用に併記

### ファイルパースの並列化

- 処遇: **見送り(条件付き)** — TODO.md には入れない(parse 支配が実測されたら再検討)
- 現状
  - `minigo.go` がパッケージ内ファイルを逐次パースする
- 参考実装
  - go-scan `scanGoFiles` が errgroup + 並列度上限でパース(調査 §4)
- 未定 → 低優先・条件付き
  - 同系の棄却例あり: `plan-minigo-vm.md` で「symbol 狙い撃ちの linear file search」が ~O(n/2) にしかならないと dropped 済み
  - ただし stdlib 級 pkg では効く規模: 実測 `os`=165 files, `net/http`=71, `time`=38 — ファイル数に価値が比例する
  - 判定は素通し AST の性能測定と同じ扱い: ベンチマーク機構は増やさず、usecasefuzz realworld の大きい target で wall-time を一回きり実測
  - 発火条件: ファイル数の多い pkg を引くワークロードで parse が支配的と測れたら採用、それまでは実質見送り

## 豊かさ(新しい機能面)

### astwalk パッケージ(仮称 — 旧 declwalk 案)

- 処遇: **昇格済み**(TODO.md — consumer-first 抽出として)
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
- 教訓(重要)
  - go-scan に同名の `astwalk` パッケージが存在し #993 で削除済み — 中身は `ToplevelStructs` 1関数だけで未使用だった
  - 「先に器を作ると1関数の墓場になる」ので consumer-first: 最初の消費者が現れてから、その形で抽出する
  - 最初の消費者候補: `inspect.UsedSymbolsOf` の file walk(抽出元)、body 走査を要する実タスク、host ツール
  - 対照的に modulewalk は go-scan 側に実績消費者あり(find-orphans、deps-walk)= 扱いは非対称
- 未定
  - bind path(`inspect` の増補か `minigo.dev/astwalk` 新設か)
  - 最小形だけか、pattern-hook まで入れるか

### modulewalk パッケージ(採用方向で確定)

- 処遇: **昇格済み**(TODO.md)
- 動機
  - 「root 以下の全パッケージを nested go.mod 越しに列挙」が既存の TODO 項目 → **採用 verdict**
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
- 名前
  - `modulewalk.Walker` — go-scan 側の `ModuleWalker` は stutter なので型名は `Walker` に落とす
  - `Visitor` はそのまま `modulewalk.Visitor` で揃う
- 未定
  - 上の「軽量パース段階」が先に要る
  - script に公開するか、host ツール専用か

### ImportManager 相当

- 処遇: **昇格済み**(TODO.md — host 実装+薄い intrinsic)
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

### gentest(旧 scantest 案)型のテストハーネス + FileWriter

- 処遇: **昇格済み**(TODO.md — 挙動確認系限定)
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

- 処遇: **未昇格(採否未決)** — 必要な局面(REPL/反復のみ)とコスト(≈0)は見えているが採用 verdict は未だ。TODO.md には入っていない
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
