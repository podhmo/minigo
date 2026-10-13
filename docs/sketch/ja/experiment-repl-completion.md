# REPL 補完 — 実験レポート

対象: `minigo repl` の補完機能。UI（行編集）ではなく、**処理系の機能**として「何が必要か」を設計し、`(*REPL).Complete(line string) []Candidate` というプロトタイプで実現可能性を検証した。独立実験。マージするかは未定。

問い:

1. 既存の言語のインタラクティブシェルの補完は何を提供しているか
2. minigo でそれを実現するには、処理系に何が必要か（`go/types` を使わずにやれるか）
3. 実際に動くか

結論: **実現できる**。`go/types` を持たないインタプリタでも、ディスパッチのために既に存在しているメタ情報機構（メソッド集合・埋め込み解決・host リフレクション）と、package Index + Globals を組み合わせれば、実用に足る補完が組める。キーとなる発見は2つ:

- **REPL はローカルスコープを持たない**（全ての名前がパッケージグローバルに hoist される）ので、補完の最大の難所である「ローカル変数の型」がそもそも存在しない。裸名の候補列挙は VM の識別子解決順序をなぞるだけでよい。
- **実行時値がない場所でも宣言型が埋めてくれる**。`var x T` は `Cell.Typ` に typedef を刻む、外部パッケージの `var s T` は `ValueSpec.Type` が index に残る、`var u S` の未書き込みフィールド `u.Builder` も `fieldTypes` が宣言型を返す。IPython の evaluation ポリシー軸で言う "minimal"（AST 走査のみ・eval しない）で十分に効く。

確認: `complete.go`（約500行）+ `complete_test.go`（10ケース）を実装。`minigo repl` に `:comp <text>` というデバッグコマンドを足して手動確認もできるようにした。`make format` / `make lint` / `make test` クリーン。

---

## 1. 既存のインタラクティブシェルの補完

調査対象と、それぞれが何を提供しているか:

| 処理系 | 補完の実装 | 情報源 |
|---|---|---|
| gore ([x-motemen/gore](https://github.com/x-motemen/gore)) | コード補完は**全て gopls に委譲** — セッションのソース + 入力行を仮想バッファに合成し、カーソル位置で `gopls` の completion を呼ぶ。コマンド補完のみ自前 | go/types（静的解析） |
| yaegi | **補完なし**。`interp.Symbols(importPath)` というパッケージの名前列挙機構だけがある（= 実行時のシンボル表） | 実行時のシンボル表 |
| Python (`rlcompleter`) | `global_matches`（キーワード + `__builtin__` + namespace）と `attr_matches` — 後者は**ベース式を eval して** `dir()` を取る。`expensive.` で副作用が走る | 実行時 namespace + eval |
| IPython | Jedi による静的解析 + `Completer.evaluation`（`"forbidden","minimal","limited","unsafe","dangerous"`）で eval してよい度合いを選択可能 | 静的解析 + 任意評価 |
| Node REPL | eval ベース — ベース式を実際に評価して `Object.getOwnPropertyNames` 相当を取る | 実行時 + eval |
| Julia (`REPLCompletions`) | ランタイムの型情報 + `propertynames` + モジュール名 + パス/LaTeX/メソッド補完まで揃う | 実行時 + 推論 |
| JShell | javac の `SourceCodeAnalysis.completionSuggestions` — コンパイラの解析機構をそのまま使う | コンパイラ |
| gopls | `go/types` によるコンパイラグレードの型情報 | 静的解析 |

### 軸の整理

この調査から、REPL 補完は3つの軸で整理できる:

1. **情報源** — 静的解析（go/types, Jedi, javac）か、実行時状態（namespace, `dir()`, reflect）か、その両方か。静的解析は「ソースの真実」、実行時系は「セッションの真実」を見る。
2. **評価ポリシー** — 補完のためにユーザーコードを評価してよいか。Python 系は attr 補完で素直に `eval()` するので `slow_func().` でその関数が走る。IPython はこれを enum で明示的に切り替える設計になっていて、参考になる。静的解析系は一切実行しない。
3. **補完対象の種類** — 裸の名前 / セレクタ（`x.`）/ import パス / ファイルパス / コマンド / 引数の型に応じた補完。

**重要な観察**: gore が gopls に丸投げできるのは、REPL の状態を「仮想ソースに焼き直して静的解析にかける」ため。しかし minigo の REPL は、再定義・`x :=` のグローバル化・`:cd`/`:pin` によるパッケージへの monkey-patch など、ソースを再構成しても追従しきれない実行時変形を持つ。つまり minigo では**実行時状態を見る側が正しい真実の源**になる。また `go/types`/`go/packages`/`go list` は AGENTS.md の制約で使えない（import が eager になるため）— 静的解析の選択肢は最初から閉じている。

## 2. minigo の補完に必要なもの

分解すると5つの部品になる:

1. **コンテキスト判定** — 入力の末尾を字句解析し、「`x.` や `x.pa` のセレクタ補完」か「`pa` の裸名補完」か「それ以外（`f(` の直後など、名前全般を出す）」を分類する。`go/scanner` の末尾2トークンで決まる。罠が1つ: scanner が自動挿入するセミコロン（`lit == "\n"`）はトークン列から捨てないと `x.pa` が誤分類される。
2. **裸名の列挙** — VM の識別子解決順序をそのままなぞる: file-scope の import 名 → Globals → Index（decls）→ 無名 import のパッケージ名 → dot import のメンバー（`:cd` の疑似 import もここに乗る）→ builtins → キーワード・predeclared。要するに「読み取り版の名前解決」。
3. **セレクタ基底の解決** — `x.y.z.` の `x.y.z` を値に解決する純粋な AST ウォーク: Ident（名前解決）、SelectorExpr（メンバー選択の読み取り版）、ParenExpr、StarExpr、整数リテラルの IndexExpr、CompositeLit（型名から `runtime.Zero`）。CallExpr や二項演算では止まる — 評価しないので。パースに失敗する途中式（`f(x.`）には suffix fallback を入れた: トークン境界で左端をずらしながらパース可能な最長サフィックスを試す。`.` はどの演算子より強く結合するので、`f(x.` の正しい基底は `x` であり、この近似は意味を持つ。
4. **メンバー列挙** — `selectMember` の読み取り版。ディスパッチ用に既に存在するメタ情報機構を再利用する:
   - `methodFuncs` / `methodSetOfU` — 宣言メソッド + 昇格（`EmbedSpecs` を `resolveTypeRef` で解決）+ interface の要求メソッド + `HostNew` の reflect セット
   - `fieldTypes` — フィールドの宣言型（値が nil のスロットの穴を埋める）
   - `hostMember` 相当 — ホスト値の reflect フィールド/メソッド列挙（`CanInterface` で未エクスポートを除く）
   - `Package` / `ImportRef` — Globals + Index。ただし **`EnsureReady` は呼ばない** — パッケージ init を走らせるのは副作用。`MemberV` の `LazyInit` 分岐と同じ制約（func/type は materialize 可、var/const は init が要る → 宣言型にフォールバック）。

5. **import パス列挙** — `import "...` の文字列コンテキストでは importable パスの列挙が要る。`go list` は使えないので手で集める: `e.binds`（バインド済み stdlib）、GOROOT/src のディレクトリ走査（`_`/`.`/`internal`/`testdata`/`vendor`/`cmd`/`builtin` を prune、main パッケージを除く）、go.mod の require と自モジュール配下のパッケージ（locator が既に go.mod/root/modulePath を持っている — `Requires()` だけ公開した）、そして REPL 独自の `./`/`../`/`/abs` ディレクトリ import。

加えて候補のメタ情報: `Candidate{Name, Kind, Detail}`。Kind は `func/method/field/var/const/type/package/keyword/builtin`、Detail は `TypGoSpelling` / `DisplayName` でレンダリングしたシグネチャや宣言型（import パスでは `bound`/`stdlib`/`module`/`dir` の由来タグ）。UI がグループ化やアイコン表示に使える粒度。

## 3. プロトタイプ

- `complete.go` — `(*REPL).Complete(line string) []Candidate`。分類 → 裸名列挙 or 基底解決 + メンバー列挙 or importable 列挙 → prefix フィルタ → ソート。
- `cmd/minigo` — `:comp <text>` デバッグコマンドを追加（行編集 UI は今回のスコープ外。liner/readline 系を入れれば TAB で駆動できる）。

実際の出力（`var u inspectpkg.User` の後で）:

```
>> :comp u.
field   Age     int
field   Base    *inspectpkg.Base
field   Builder strings.Builder
method  Bye     func() string
method  Greet   func() string
field   ID      int64          ← 昇格（*Base は nil だが typedef から分かる）
field   Name    string
>> :comp u.Builder.Write
method  WriteString  func(string) (int, error)   ← nil のフィールドも宣言型で辿る
```

import 補完の実際の出力（testdata/ から）:

```
>> :comp import "str
package strconv    bound
package strings    bound
package structs    stdlib
>> :comp import "github.com/podhmo/
package github.com/podhmo/minigo/resolve    module
...                                    （自モジュール配下 + require）
>> :comp import "./
package ./inspectpkg    dir
...                                    （cwd 配下の Go ファイルを持つディレクトリ）
```

テスト（`complete_test.go`、11 ケース、全て pass）:

| ケース | 内容 |
|---|---|
| bare names | builtins / keywords / predeclared / `x :=` のグローバル |
| package | `import "strings"` → `strings.` に `Contains`/`Builder`/`NewReader`、prefix フィルタ |
| struct + methods | `type Pair struct{...}` + メソッド → `p.`（フィールド+メソッド）、`Pair.`（メソッド式） |
| named basic | `type MyInt int` + メソッド → `m.`/`MyInt.`（`Cell.Typ` 経由 — `var m MyInt` のゼロは coerce で Named タグされることを確認済み） |
| enum | `type Color int` + `const Red Color` → `Color.` に Red/Blue。REPL では const は Index ではなく Globals の ReadOnly cell（Typ スタンプ済み）に載る — index 走査と Globals 走査の両方で拾う |
| host type | `var b strings.Builder` → `b.Write*`（`HostNew` の reflect セット） |
| dir package | `import "./inspectpkg"` → メンバー列挙・非公開の隠蔽・`inspectpkg.User.` のメソッド |
| chain + embeds | `u.Builder.`（nil フィールドの宣言型フォールバック）、`u.I`（nil 埋め込み `*Base` 経由の昇格フィールド）、`println(u.`（suffix fallback） |
| dot import | `import . "./inspectpkg"` → 裸名に `Hello` |
| slice index | `xs[0].` |
| import paths | `import "str` → bound/stdlib、`github.com/...` → module/requires、`import "./` → ディレクトリ、エイリアス・dot 形式 |

## 4. 限界（この方式の穴）

- **呼び出し結果は見えない**: `f().`/`m[k].`/`a+b.` は基底を解決できないので候補なし。eval ベースの completer なら出る。minigo には `Engine.EvalExpr`（`OpEvalAST` ブリッジ）が既にあるので、IPython 式に評価ポリシーを引数化すれば "minimal" の上の段を足せる。ただし eval はユーザーコードの副作用を走らせる — 対話の補完ではデフォルト minimal が妥当と考える。
- **`var x = f()` の型推論なし**: 宣言型のない var は値を見るしかない。REPL 自身の名前は hoist された実値があるので解決できるが、未 init の外部パッケージの `var s = compute()` には静的にも実行時にも型がない。
- **文字列コンテキストは import と meta-command の引数のみ**: map key は対象外。meta-command の引数は `CompleteCommandArg` が扱う — `:load` は `.go` ファイルとディレクトリ（末尾 `/` 付きで次の TAB が潜る）、`:unload` は現在のロード、`:cd`/`:ls`/`:doc` はセッションの import 名・import パス・`./` ディレクトリ、`:doc name.Sy` はパッケージのメンバー、`:comp` は引数をコード断片として補完する（ディレクトリはその場で読み、キャッシュしない）。`:` 行そのものの補完はフロントエンド側の仕事（コマンド表は cmd/minigo にしかない）。importable 集合の列挙も近似: GOROOT は「ディレクトリに非 main パッケージがある」判定でビルド制約までは見ない、`replace` の影響は require の綴りだけ反映（import パスは module path のままなので実害なし）、`internal` は一律 prune（本来は近接ルール）。セッション中に作ったディレクトリは `importCands` キャッシュの関係で出ない。
- **same-depth の昇格曖昧性**: 本物は compile error だが、補完は最初のヒットを取る（候補を出す側は審判ではない、という割り切り）。
- **過剰なメソッド候補（値コンテキストのみ）**: 値基底では `methodFuncs` を ptr=true/false 両方で呼ぶので、文脈上取り得ないレシーバのメソッドも出ることがある。REPL の変数は全部 addressable なので実害は少ない。型レベル基底（`T.`）は method expression なので値メソッド集合だけに絞ってある（`func (p *T) M` は `T.M` では trap する）。Named host box に宣言メソッドがある場合も reflect セットが併記される（実行時は declared-only）— 補完として多めに出すのは害が少ない判断。
- **昇格メソッドの Detail**: 宣言元ファイルのスコープで `TypGoSpelling` を呼ぶので、selector 修飾された型が曖昧に見えることがある。
- **suffix fallback の誤爆**: `(u.` は正しく `u` のメンバーを出すが、`m[k.`（開いた index 式の中の `.`）も `k` のメンバーを出してしまう。`.` 末尾のコンテキストを未閉鎖グループ内と区別するには `resolveBase` 側の括弧深度追跡が要る — TODO に積んだ。
- **adapter 系 builtin は Detail が空**: `strings.Compare` など `fn*` ラッパーは `Target` を持たないので reflect シグネチャを描けない。嘘の `func(...) any` より空の方がまし、という判断。

補足 — レビューで見つかった「読み取り版と selectMember の意味論差分」は修正済み（いずれもミラーする側の見落としで、方式の限界ではない）:

- `type A B` は B のストレージを共有するがメソッド集合を継承しない。値は `Named{A, Struct{Def:B}}` なので、内側の Def までなめると B のメソッドが漏れる — フィールドは内側 Def（実レイアウト）から、メソッドは宣言側 typedef から取るようにした。
- host reflect のフィールドを `NumField` の直舐めにしていたので昇格フィールドが抜けていた。`reflect.VisibleFields`（昇格を flatten し、影になる深いフィールドを落とす）に差し替え。
- `type T []E` のフィールドは `Named` tag 付きで格納されるので、`x.F[0].` の index 解決が Deref の時点で詰まっていた。Unwrap を差し込んで解消。

## 5. 考察 — 「処理系の機能として何が必要か」の答え

調査とプロトタイプから、インタプリタの補完に必要なのは:

1. **名前解決の読み取り版** — VM の ident 解決（Scopes → Globals → Index → imports → dot imports → builtins）を副作用なしでなぞる層。minigo は解決順序が VM に定義済みなので、ミラーするだけで意味論が一致する。
2. **メンバー列挙** — `selectMember` の型種別ディスパッチを「列挙方向」に展開する。ここが一番の資産だった: メソッド集合・埋め込み・host reflect・typedef 解決は duck-typing のために全部実装済みだった。
3. **宣言型のサルベージ** — 動的言語の補完が「値があるところでしか効かない」のに対し、`var x T` の `Cell.Typ`、`var s pkg.T` の `ValueSpec.Type`、`fieldTypes` のフィールド宣言型が「値がなくても答えられる」領域をかなり広げる。静的型検査器なしの貧乏人の型推論。
4. **評価ポリシーの切り分け** — 「eval してよいか」は API のポリシー引数であり、completer のコアは eval しない設計にしておく。IPython の `evaluation` enum がこの設計パターンを明文化している。

そして REPL 固有の幸運: 全ての名前がパッケージグローバルなので、一般の言語で一番難しい「ローカルスコープの変数の型」を考えなくてよい。minigo の補完は「パッケージメンバー列挙」と「値のメンバー列挙」の相互再帰だけで構成できる。

実装コストは約500行 + テスト。機能の大半は既存機構の呼び出しで、新規の解析ロジックはほぼゼロ — 処理系の内部表現（Index/typedef/Globals）がそのまま補完のデータ源になったというのが、今回の実験の最も重要な確認だと思う。

## 6. 残タスク

TODO.md の "REPL completion" エントリに列挙。大きいのは:

- 行編集 UI（liner/readline 系）との接続 — `Complete` は処理系 API、TAB で駆動するにはフロントエンドが要る
- eval ポリシー階層（`f().` など呼び出し結果の補完）
- 残りの文字列コンテキスト（`:cd` 引数・map key）
