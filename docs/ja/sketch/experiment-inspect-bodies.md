# inspect の関数ボディと浅い解析の実験

## 計画（実装前）

目的は、関数の文・式をスクリプトから読めるようにし、HTTP ハンドラーのリクエストパラメーター候補を補助関数越しに推測する際に必要な inspect の情報を確認すること。OpenAPI の完全な生成や Go の完全な型検査は目標にしない。

現状の調査では `index.Decl.Func` は `*ast.FuncDecl` を保持し、その `Body` も存在する。TODO の「index/syntax stop at the signature」は公開 API の説明としては正しいが、内部 AST がボディを捨てているという意味では正しくない。実行・初期化せずに取り出せる。

### 実装するもの

1. `inspect.Body(decl)` と `inspect.SyntaxChildren(node)` を追加する。ノードは Kind、Text、Pos、親からの Role と Index、Token、宣言 Owner を持つ。子は AST の直接の子のみ。AST 自体や parser の Object リンクは公開しない。
2. 明示的な型位置のノードに宣言ファイルに結びついた TypeExpr を付ける。式の型推論とは区別する。既存の Children(TypeExpr) は変更しない。
3. スクリプトからボディを再帰走査するテストを作る。読み取り時に対象の init、依存パッケージ、ハンドラーが実行されないことを確認する。
4. `experiments/httpinspect` に再現可能な小さい解析器を置く。生 AST を使わず、このビューから文を浅く解釈する。抽象値（request、URL、query、パラメーター由来、文字列定数、unknown）を引数・戻り値へ伝播する。
5. 同一・別パッケージの直接補助関数を必要時だけ開く。標準ライブラリは境界で止め、`Request.URL.Query().Get`、`Header.Get`、`PathValue`、`strconv.Atoi` を要約する。任意の標準ライブラリ関数は実行しない。
6. 分岐は両側を調べて候補を集める。必須性は推定しない。再帰は深さ・解析ステップの上限で止める。動的キー、未対応構文、未解決呼び出しは診断として残す。

### 実験ケースと判定

- 直接の query/header/path アクセス、補助関数へ request とキーを渡すケース、戻り値を整数変換するケース。
- import alias と別パッケージ補助関数、同名ローカルによるシャドーイング、分岐、再帰、動的キー、ループ。
- パラメーター名・in・schema 候補を期待値と比較し、診断・呼び出しトレース・対象の indexed 状態を検証する。
- 未対応を空の成功として扱わない。出力は OpenAPI の parameter 候補のリストと incomplete を持ち、paths や responses の揃った OpenAPI 文書とは呼ばない。

### 比較する設計

構文走査だけでは補助関数に渡される request やキー、戻り値の由来が分からない。通常 VM の実行追跡は一つの具体的な実行経路しか観測できず、初期化や I/O を避ける境界も必要になる。専用の解析 bytecode は有望だが、まず AST ビューで抽象解釈を試し、必要な意味情報が判明してから検討する。フックだけで通常 VM の値を unknown に差し替える設計にはしない。

## 実施記録

以下に実装後の観測、計画との差、意思決定、検証結果を追記する。

### 実装結果

実装先は `inspect/body.go`（ビュー）、ルートの `inspect.go`（intrinsic）、`experiments/httpinspect/analyze.go`（解析方針）、同ディレクトリの CLI と testdata。日本語レポートの出力先は指定通り `docs/ja/sketch` とした。既存レポートの `docs/sketch/ja` とは別の場所である。

`Body` は宣言を入口にして BlockStmt を返す。ソースにボディがない宣言は nil、host の関数や非関数はエラーになる。`SyntaxChildren` は直接の子だけを返し、例えば CallExpr の `Fun` と `Args`、AssignStmt の `Lhs` と `Rhs` を Role で区別する。Index はリスト内の添字、単一要素では -1。Token は `:=`、演算子、リテラル種別など。AST のコメントや Object の逆参照は辿らない。メソッド、FuncLit、select 等を含む構造も失わずに走査できる。

Type は `var local *web.Request` のような明示的な型位置だけに付く。`web` という import alias は宣言ファイルに結びついた TypeExpr から `net/http.Request` に解決できた。`r.URL` の型や、ローカルな `type T` の束縛まで解決する API ではない。ボディ内の任意の Ident に既存 SymbolID を適用するとパッケージの名前と混同する可能性があるため、自動で式全体を TypeExpr に変換する設計は採らなかった。

スクリプト実験は `testdata/inspectbodyuse`。実際の minigo スクリプトが Body と SyntaxChildren を再帰呼び出しし、Helper 内の3つの CallExpr を数え、Role/Index/Token/Pos/Owner を読み、ローカル型の alias を解決する。対象パッケージは indexed のまま。host/non-function/引数個数違いのエラーもテストした。

### HTTP 解析で得られたもの

fixture は通常の `net/http` 形式のハンドラーを新しく作った。対象も別パッケージの補助関数も init に panic を置いている。解析器は実 AST を直接走査せず、公開した Node のビューと既存の Signature/Imports を使う。関数名の検索に既存のパッケージ index を使い、呼び出されたソース補助関数のみ Engine.Package で読む。一般的なスクリプト解析器の完成形を実装したわけではなく、構文ビューの妥当性と意味解析の必要情報を host 側の小さなプロトタイプで調べた。

値の経路の例:

```text
Handler: request, "limit"
  -> same-package query(request, "limit")
  -> helpers.Query(request, "limit")
  -> Request.URL -> URL.Query summary -> query.Get summary
  <- parameter@query:limit
  -> helpers.Identity(parameter@query:limit)
  <- parameter@query:limit
  -> strconv.Atoi summary
  <- integer@query:limit, unknown error
```

この例の出力は `name: limit, in: query, schema.type: integer, required: false` という候補で、取得箇所と変換箇所のソース位置を evidence に持つ。7つの呼び出しトレース、22ステップ、incomplete=false。request API と Atoi の境界に `boundary: true` が立ち、開いた補助関数には false が立つ。トレースは呼び出し完了順。

`Atoi` があるから整数以外を拒絶していると証明したわけではない。エラーを無視している fixture でも整数候補になる。文字列としても使う場合の競合、入力の検証、fallback、デフォルト値は将来の解析が必要。query/header の required=false は必須性を証明した結果ではない。path の候補だけ true にする。この出力には routes、responses 等がなく、完成した OpenAPI 文書ではない。

分岐では別々の環境を保持し、両側の候補と戻り値を集める。戻った経路は後続文を実行しない。戻り値の由来を union できるので、早期returnの分岐から返された2つの query 候補にも Atoi の情報が伝わる。条件式自体は unknown として診断する。到達可能性を証明せずに候補を収集する過大近似である。

### 想定外の出来事と意思決定

1. **内部 AST は既にボディを持っていた。** 新たなパーサーや通常コンパイラーの変更は不要だった。TODO の記述を実態に合わせて修正した。
2. **子のリストだけでは役割が足りなかった。** Role/Index を追加した。Go AST の構造体フィールドを反射で読み、AST ノードだけを包む方式にした。全構文ごとの巨大な switch は不要になる一方、フィールド名がAPIの語彙になる。固定的な解析IRとは別物として扱う。
3. **型位置と値の束縛は別問題だった。** 宣言ファイルの import alias は既存 TypeExpr で解決できるが、ローカル変数、キャプチャ、メソッドの呼び出し先は解決できない。今回の抽象環境は名前を管理し、import とローカルのシャドーイングを区別する。inspect の最終的な束縛解決機能ではない。
4. **標準ライブラリを止めても戻り値の要約が必要だった。** Query の結果を単なる unknown にすると後段の Get を認識できない。`request -> url -> query -> parameter` の要約が最低限必要だった。未知の標準関数は opaque 診断と unknown 戻り値にする。
5. **未対応文を飛ばすだけでは古い値を誤用した。** ループが key を変更した後も以前の定数を保持する可能性があったため、未対応文の後ではローカル値を unknown にするよう変更した。ループ内部のアクセスも推測せず、incomplete を返す。ヒープやグローバルへの影響まで安全に要約するものではない。
6. **return と scope を考えない走査では由来が混ざる。** 経路ごとの returned 状態を追加し、ブロックと if init のシャドーイングを戻すようにした。名前付き戻り値も試した。完全な binder はまだ必要。
7. **AST からの Text 生成にもコストがある。** 子ビューは必要時に作るが、Text は要求された部分木全体を printer に渡す。親と子の繰り返し要求で大きな関数には重複作業がある。深さ/ステップ上限はこの printer の処理量や AST のメモリーを制限しない。性能測定・キャッシュを TODO に残した。
8. **環境のキャッシュ書き込みが制限された。** 最初の go test は Go の既定キャッシュ、lint は staticcheck の既定キャッシュへの書き込みで失敗した。GOCACHE と STATICCHECK_CACHE を `/tmp` 下に指定して再実行した。GOPATH は変更していない。

### 通常VM、専用VM、構文ビューの判断

通常VMは `OpCall` で実際に呼び出し、`OpJumpFalse` は具体的な truthy 判定で片方へ進み、パラメーターや戻り値には `OpCoerce` 等で通常の型処理を行う。呼び出しログを足せば具体値の観測はできるが、unknown の両分岐を調べたり、初期化を避けたり、抽象値を型変換に通したりする意味論は得られない。比較テストでは、通常の Engine.Run で同じ fixture に入ると handler の init panic に到達した。

今回の実験では専用 bytecode はまだ実装しなかった。浅い解析のために必要なのは命令の表現を変えることより、抽象値と呼び出し境界の契約だったため、まず inspect の構文ビューで試した。通常VMにも変更は加えていない。

専用VMを次に試すなら、通常の bytecode を無理に流用せず、次の少数の操作へ lower する案がよい:

- Bind/Assign: 束縛IDと値の由来を移す。
- Project: request.URL 等の抽象プロジェクション。
- Call: 引数を記録し、source 関数を開く／summary を使う／opaque にする、をポリシーで選択。
- Return: 複数戻り値とパラメーター由来を記録する。
- Branch/Join: unknown 条件で状態を分け、合流で由来を union する。
- Unsupported: 診断と保守的な状態の無効化。

こうすれば通常の実行可能性や標準ライブラリの完全な解釈を前提にせずに済む。一方、lowering には lexical binding と制御フローが必要であり、bytecode を作るだけではメソッド・クロージャ・動的呼び出しの解決は進まない。繰り返し解析やループの不動点計算を試す段階で、キャッシュ可能な関数IRを導入する価値が大きくなる。

### inspect に必要なものの整理

| 層 | 今回の結果 | 次に必要なもの |
| --- | --- | --- |
| 構文入口 | Body と直接の子ビューでスクリプトからアクセスできた | Text の生成コストとビューの安定性 |
| 構文の関係 | Role/Index/Token/Pos が最低限有用 | 文単位の制御フローを必要時に提供するかの検討 |
| 型の文脈 | 明示型と宣言の Owner で import alias を維持できた | ローカル型、式の型、型aliasとメソッド集合 |
| 名前の文脈 | 実験側の環境で直接関数とシャドーイングを扱えた | 宣言/参照の束縛ID、scope、capture、call target の候補 |
| 意味の解釈 | request とキーと戻り値の由来を伝播できた | summary registry、再帰summary、ループの不動点、ヒープ効果 |
| 不確実性 | opaque/dynamic/unsupported/budget を診断に残した | 推論根拠の競合、到達可能性、契約としての確度 |

HTTP の知識や探索深さを inspect 本体の仕様に入れる必要はない。inspect は実行しない構文と文脈を渡し、解析側がどこまで開くかと何を要約するかを決める設計を推奨する。

### ケース別の観測値

CLI を各 fixture に対して実行した値。ステップ数は抽象操作のカウンターであり、実時間や Go AST ノード総数ではない。

| ケース | 候補 | ステップ / 呼び出し | incomplete / 主な理由 |
| --- | --- | --- | --- |
| Direct | query:search、header:X-Token、path:id | 15 / 5 | false |
| Helper | query:limit (integer) | 22 / 7 | false |
| Branch | query:left/right/after、header:X-Local | 76 / 19 | true / 条件式unknown |
| Recursive | query:before-depth | 106 / 29 | true / 深さ上限 |
| Dynamic | header:X-Key、query名は確定せず | 19 / 6 | true / 動的キー |
| Loop | なし | 1 / 1 | true / ForStmt未対応 |
| Opaque | query:opaque | 15 / 6 | true / strings.TrimSpaceをopaque扱い |
| BranchResult | query:early/late (integer) | 36 / 11 | true / 条件式unknown |
| ShadowImport | header:X-Shadow | 7 / 2 | false / import名をローカルが隠す |
| IfShadow | header:X-Inner、query:inside/outer | 53 / 14 | true / 条件式unknown |
| NamedResult | query:named (integer) | 20 / 7 | false / 名前付きreturn |
| UnknownMutation | なし | 17 / 5 | true / 未対応文で値を無効化 |
| Closure | なし | 4 / 2 | true / FuncLitと動的呼び出し未対応 |

`TestLazyBoundaries` の resolver の記録は handlers、helpers の2パッケージだけと完全一致した。net/http、strconv、strings はこのケースでは locate すらされない。対象および補助関数のパッケージ状態も indexed に留まる。単にネットワークを実行しなかったというだけでなく、標準ライブラリの実装を開かずにパラメーター由来を追跡できた。

### 再現方法

リポジトリルートで実行する:

```sh
go run ./experiments/httpinspect/cmd/httpinspect \
  github.com/podhmo/minigo/experiments/httpinspect/testdata/handlers Helper

go test ./experiments/httpinspect ./inspect ./ \
  -run 'Test(Analyze|Budget|InspectBodyScript|BodyShapes|LazyBoundaries|ConcreteExecutionBaseline)$'
```

キャッシュ書き込みが制限される環境では以下を使用した:

```sh
GOCACHE=/tmp/minigo-body-go-cache make format
GOCACHE=/tmp/minigo-body-go-cache STATICCHECK_CACHE=/tmp/minigo-body-staticcheck-cache make lint
GOCACHE=/tmp/minigo-body-go-cache make test
git diff --check
```

## 検証結果

- `make format`: 成功（goimports）。
- `make lint`: 成功（staticcheck）。
- `make test`: 成功。ルート全体と examples/task-run、examples/convert-define を含む。
- `git diff --check`: 成功。
- 13 fixture の候補と incomplete の期待値、呼び出しの戻り値トレース、ステップ上限、ソース読み込み境界、通常実行との初期化の違い、スクリプトからのボディ走査と型文脈をテストで検証した。

残件は TODO.md にチェックボックスで追加した。完了した「関数ボディの公開＋浅い解析の実験」は Implemented セクションにまとめた。完全な OpenAPI 生成、専用解析VM、完全な名前・型解決は未実装として区別している。
