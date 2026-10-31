# minigo の位置付け —— 何であり、何ではないか

リリースに向けた整理メモ。minigo が取る領域（生息区域）と、取らない領域、それに伴う機能の方向性をまとめる。実装の実態は README.md と `docs/ja/minigo-architecture.md` が正本で、ここは「なぜその形なのか」の側。

## 評価軸

スクリプト実行の体験速度は **ビルド速度 + 起動速度 + 実行速度の合算** で決まる。

- ビルドはキャッシュが効く。2回目以降の `go run`/`go build` は速い。
- 実行速度はネイティブ Go が無条件に勝る（minigo は JIT のないバイトコードインタプリタで、ネイティブ比 ~100〜150 倍が上限側の実測。`docs/sketch/ja/experiment-gosimilarity.md` と追試の `experiment-gosimilarity-reverify.md` —— 後者は wall で約150〜165倍）。

すると minigo の領域は自ずから「**キャッシュが効かない初回ビルドの遅さへの苛立ち**」への対処に絞られる。インタプリタ自体は Go でビルド済みなので、minigo 上でスクリプトを実行する経路にはビルドが存在しない —— これが価値の依存点。

## 取る領域・取らない領域

| 領域 | 判定 | 理由 |
|---|---|---|
| 入力が変わり、その都度実行するスクリプト | ○ 対象 | コード生成が代表例。stringer 的な引数駆動、gqlgen 的な設定ファイル駆動 |
| `io.Copy` や `[]byte`→`[]byte` の gofmt 的処理 | ○ 速い | 両端がホスト値ならネイティブ直通（README "Performance notes"） |
| eager 全読み込み + 完全型チェック（`go/packages`/`go/types` 型） | × 拒絶 | 起動コストが pitch と矛盾。部分的な型情報しか扱わない設計（AGENTS.md でも禁止） |
| リポジトリ全体のフルスキャン | × 範囲外 | AST 解析だけでも実行速度で律速。gosimilarity で実測済み（同上） |
| 常駐デーモン・フルセットのツール提供 | × 範囲外 | Go 製で十分。minigo は「毎回起動して即終わる」実行 + インクリメンタルな機能提供に絞る |

### AI エージェントとの競合

生成そのもの（scaffolding / vibe coding）は AI が便利で、minigo の領域ではない。残るのは **制約充足のチェックと同期** —— AI にこれをやらせると入力トークンに線形にコストがかかる。ここに Go 製ツール、または minigo スクリプトが入る。チェックが浅い（型の完全な検査を要しない）なら minigo で十分、という線引き。

## 機能の方向性

- **単純な main 実行だけでは弱い** → 柔軟なエントリーポイント指定が軸。`Engine.Run(ctx, ref, fnName)` の ref は import path / dir / `.go` ファイルの3形態で、「読む箇所と読まない箇所」を呼び出し側が分けられる（`docs/ja/minigo-engine.md`）。
- **quoted go（特殊フォーム）** —— スクリプト内で Go 構文に触れられる。`OpSpecialCall` が呼び出しを AST のままホストハンドラに渡し、`SpecialContext.Quote/Eval/Resolve*` が quoted なコード片を操作する（`examples/convert-define`）。
- **スタブで済ませる設計がコア**。LSP/fmt 等の再実装はしない。`minigo.dev/host` や `inspect/stub.go` のように本体が `panic("minigo intrinsic")` の宣言だけを置き、gopls/gofmt が効く静的に正しい Go のまま実行時は intrinsic が横取りする。解釈を変えれば同じ stub が minigo 側・Go 側双方への機能提供になる（`minigo vet` は未登録 stub 呼び出しの静的検出）。
- **不足している部分**: code walk（AST/宣言の走査 —— `inspect` が半分）、code writer（生成・書き戻し —— `gen-sync` の managed block が最初の形）、LLM に渡す情報断片の収集・出力。可視化は AI 任せで十分。

## なぜリリースするのか / してこなかったのか

リリース（バージョン付け）の意味は **minigo に依存したコードを書けるようになる** こと —— バージョンを固定できる。利用側は `go.mod` で pin して、壊れない組み合わせを選べる。

一方でリリースは **ユーザーから見たインターフェイスを固定する** という側面がある。ここまでリリースしてこなかったのは、モノリポの手軽さを捨てたくなかったため —— `examples/` 内での仮想的な利用（`task-run`/`convert-define`/`gen-sync`/`extract-schema`/`minigo-generate`）でフィードバックを集める形を取っていた。インターフェイスを固定してしまうと、その試行錯誤の余地が狭まる。

## 今後の検討

- `repl.go`/`repl_load.go` の `minigo/repl` 等への切り離し —— 現在はルート `minigo` パッケージ内。
- `go run`/`go generate` 経路の `minigo-generate` への集約 —— `//minigo:generate` directive を in-process で実行する runner（`examples/minigo-generate`）。`cmd/minigo-generate` 昇格案が `docs/sketch/plan-minigo-generate.md` にある。
- 設定の探索による SSOT（single source of truth）の維持。
- 呼び出しフックによる仮想実行 vet 的チェック —— VM の `Hooks`（`vm/vm.go`）経路に載せる形。
- `net/http` ルーティングを別文脈で評価する用途 —— `registerRoutes` のような関数 body 走査が前提（TODO.md の inspect.Body 系項目と接続）。

## 関連

- `README.md` — 機能一覧と "Currently unsupported" の境界
- `docs/ja/minigo-architecture.md` — lazy ロードと特殊フォームの内部構造
- `docs/ja/minigo-engine.md` — ref 3形態と Run/Call の使い分け
- `docs/sketch/plan-minigo-generate.md` — `//minigo:generate` の設計
- `docs/sketch/ja/experiment-gosimilarity.md` — フルスキャン負荷の実測（速度の上限側）
