# TODO: リアーキテクチャ（次期）

> リポジトリ直下の [TODO.md](../../../TODO.md) とは別に、[リアーキテクチャ計画](../../sketch/ja/plan-rearchitecture.md)（第 3 版）を実行可能な単位に分けたもの。運用は [todo-md スキル](../../../.claude/skills/todo-md/SKILL.md) に準じる。`[ ]` は未着手、`[-]` は一部完了、`[x]` は完了を表す。
>
> - チェック項目の末端を 1 PR の目安にする。親の番号は計画書や参照資料からのリンクに使い、明示した依存先から着手する。
> - 既存の意味論を固定するテスト、実装の移動、挙動や性能を変える切り替えは、個別に検証できる単位で進める。
> - 「完了条件」が満たされたら `[x]` にする。
> - どの PR でも、`make format`、`make lint`、`make test`、pin 済みの difffuzz ケース、examples を通す。共有状態やコールバックに触れる PR では `make test-race` も通す。
> - 詳細は [references/](./references/) に置いた。
> - 計画の §8 の判断（K1〜K13）を覆す場合は、先に計画書を更新する。

## M0 + P0: 振る舞いの固定とベースライン

- [x] **M0-1 `pkg/locator` の `go env` をメモ化する**: `getGoRoot`/`getGoModCache`（locator.go:474, 484）がロケータの生成ごとにサブプロセスを起動しており、最小の `NewEngine` 実行のほとんどの時間を占めている。プロセス単位で `sync.OnceValues` にする。完了条件: `NewEngine` のベンチで差分を報告している。[references/baseline.md](./references/baseline.md)
- [x] **M0-2 ベースラインを記録する**: `session_bench_test`、`genericbench_test`、`NewEngine`/`NewSession` 単体、realworld の `grafana-openapi` の実行時間と alloc を記録する。完了条件: 以降の PR が差分を報告できる表が [references/baseline.md](./references/baseline.md) にある。
- [x] **M0-3 スパイクのカウンタを常設する**: コンパイル起因のビルド数、C1 違反（コンパイル中の初期化）、`chunkCellsEscape` 率を、テスト専用のフックかビルドタグで取れるようにする。完了条件: C1 違反が 0 であることを確かめるテストが CI にある。[references/spike-instrumentation.md](./references/spike-instrumentation.md)
- [x] **M0-4 遅延性の振る舞いを pin する**: 型の問い合わせだけでは init が panic するパッケージの初期化が走らないこと、import 先の未使用分岐は初期化されないこと、を確かめる振る舞いテストを書く。完了条件: index と初期化の対象をパス単位で検証するテストがある（出力だけを見ない）。[references/ownership.md](./references/ownership.md)
- [x] **M0-5 Session の分離と編集後の更新を pin する**: 2 つの Session でカウンタが独立に進むこと、ソースを編集したあと既存の Session は旧版のまま動き、新しい Session は新版を使うこと、を確かめる。完了条件: 現状の振る舞いを記述するテストがある（違いが既知の差分なら、その旨を明記する）。[references/ownership.md](./references/ownership.md)
- [x] **M0-6 Quoted Go を pin する**: スタブパッケージがロードも実行もされないこと、別名 import、ローカルなシャドウイング、同じ綴りの別関数を正しく判別することを確かめる。完了条件: 該当する examples（convert-define）以外にもルートのテストがある。
- [x] **M0-7 コンパイル時の事実に依存する 15 件を明示する**: スパイクで答えない oracle を使うと壊れた 15 件に、「compile-time fact」のラベル（コメントかテーブル）を付ける。完了条件: 一覧が [references/acceptance-set.md](./references/acceptance-set.md) とテスト側の両方にある。
- [x] **M0-8 `bytecode.Disasm` の golden を追加する**: 代表的な関数を十数個選ぶ（クロージャ、defer、range、特殊形式、ジェネリクス、多値返却、goto）。完了条件: compile の変更が golden の diff として見える。

## M1: 所有権の構造的な分割

- [x] **M1-1 `sharedAssets` と `sessionState` に機械的に分割する**: Engine のフィールドを、ソース由来の共有物（resolver、FileSet、AST/index キャッシュ）と Session 状態（pkgs、binds、globals など）に分ける。完了条件: 公開 API と共有の規則が変わっていない。[references/ownership.md](./references/ownership.md)
- [x] **M1-2 `internal/archcheck` と `internal/cmd/archcheck` を作る**: `go/parser` だけで import グラフを検査する。最初は現在のグラフを許可集合にして、ラチェットで締めていく。完了条件: CI で動き、負例（禁止された import を足す）で落ちる。[references/archcheck.md](./references/archcheck.md)
- [x] **M1-3 共有資産のフィールド許可リスト**: 共有資産とそのキャッシュエントリのフィールド型を再帰的に検査し、実行状態（`*runtime.Cell`、`*runtime.Function` など）が入ったら落とす。完了条件: 負例と正例のテストがある。[references/archcheck.md](./references/archcheck.md)
- [x] **M1-4 ファイルサイズのラチェット**: 生成ファイルでもテストでもない `.go` ファイルが記録済みのサイズを超えたら失敗させる。完了条件: 記録表があり、予算は縮む方向にしか変わらない。

## P1: ファイル分割（機械的。ロジックの変更はゼロ）

- [x] **P1-1 `vm/vm.go` を分割する**: `// ----` セクションに沿って `loop.go`、`frame.go`、`call.go`、`panic.go`、`trace.go`、`sched.go`、`ffi_marshal.go`、`ffi_iface.go`、`const.go`、`binop.go`、`convert.go`、`assert.go`、`generic.go`、`special.go`、`select.go`、`composite.go`、`iter.go` に分ける。完了条件: `git diff --color-moved=dimmed-zebra` で移動だけであることを確認している。
- [x] **P1-2 `compile/compile.go` を分割する**: `scope.go`、`stmt.go`、`assign.go`、`expr.go`、`call.go`、`lit.go`、`constfold.go`、`typehint.go`、`goto.go`、`special.go` に分ける。完了条件: P1-1 と同じ。
- [x] **P1-3 `intrinsics.go` を import パスごとに分割する**: `installStdlib` を `installStrings(h)` などの呼び出しを並べるだけの関数にする。完了条件: P1-1 と同じ。
- [x] **P1-4 gosimilarity の R1〜R6 を取り込む**（R1・R2・R6 は PR 済み、残り R3〜R5）: scratch worktree で検証済みの統合（正味 −452 行、`(*TypeDef).TypeExpr()` を含む）を入れる。[../../sketch/ja/experiment-gosimilarity-refactoring.md](../../sketch/ja/experiment-gosimilarity-refactoring.md)

## M2: semantics の問い合わせ

- [x] **M2-1 `internal/semantics` の骨格を作る**: `TypeRef`、`Policy`（`IndexedOnly`/`AllowIndex`）、`Result`（Found/Missing/Deferred/Failed）を置く。`runtime`、`vm`、`compile` は import しない（archcheck に規則を足す）。完了条件: 型宣言の問い合わせが 1 つ、4 種類すべての結果についてユニットテスト付きで動く。[references/semantics-api.md](./references/semantics-api.md)
- [x] **M2-2 `internal/source` に Indexed ビューを用意する**: `semantics` から見えるのは `IndexedOnly`/`AllowIndex` のビューだけにし、初期化を呼べないようにする。`ImportRef.Materialize`（実際には index している）を、名前で段階が分かるアダプタで包む。完了条件: semantics から `EnsureReady` 系に到達できないことを archcheck が保証している。
- [x] **M2-3 コンパイラの 8 箇所をヒントと事実に仕分ける**: 仕分けの草案は [references/fact-sites.md](./references/fact-sites.md) にある。`DisplayName` 経由で到達する `runtime/typedef.go` の呼び出しも含める。完了条件: 各箇所にヒントか事実かを書いたコメントとテーブルがある。
- [x] **M2-4 8 箇所をコンパイラ用アダプタ経由にする**: nil/false で表していた「不明」を `Result` に置き換える。Deferred になった事実は、当面は今と同じく `AllowIndex` で即時に答える（振る舞いは変えない）。完了条件: `compile/` から `ImportRef.Materialize()` を直接呼んでいる箇所が 0 になり、archcheck がそれを禁止している。

## M3: 実行時チェック

- [x] **M3-1 軽量な所有権チェック**: 他の Session の PackageInstance を、初期化や呼び出しの前に拒否する。実装は owner が設定された package に対し `Engine.Call` の入口で常時有効（開発時限定ではない）。完了条件: 誤用テスト（Session A の dep を Session B の Call に渡す）がある。[references/ownership.md](./references/ownership.md)
- [x] **M3-2 資産監査をオプトインで分ける**: AST/index の改変を検出する。D-own の計測ではリロード+呼び出しが約 2〜3 ms、完全ハッシュ付きでは約 1〜1.6 s なので、既定では無効にする。完了条件: 通常の実行ではハッシュのコストを払っていない。

## P2: パッケージの抽出

- [x] **P2-1 capability インタフェースを定義する**: `VMCaller` を union として残す。隠れた capability（`HostClear`、`HostMapDelete`、`HostNilTwin`、`MaterializeConstErr`、`CallCatching`）も含める。完了条件: 型アサーションで呼んでいた箇所が、インタフェースのメソッド呼び出しになっている。[references/vmcaller.md](./references/vmcaller.md)
- [-] **P2-2 受け渡し先の helper を capability 型にする**: `runtime.IfaceMember`、`hostErrOf`、`display`、`callOrPanic`、`goJSONSeen` などが対象。完了条件: caller を受け渡すだけの 184 関数が、本当に必要とする capability に絞られている。
  - [x] JSON/io/errors/maps/seq 系の中核 helper を絞った（`IfaceMember`→`MemberLookup`、`hostErrOf`→`MemberCaller`、`goJSONSeen`→`MemberShapeCaller`、`callOrPanic`→`ScriptCaller` など）。
  - [x] メソッド別の原子 capability（`ValTyper`/`Converter`/`Equaler`/`ElemZeroer`/`ArraySizer`/`MapKeyer`/`NewMapper`/`Copier`）と合成型を追加し、中間層の受け渡し関数を絞った。`fmtValue.c`/`scriptLocker.vc` フィールドも `MemberCaller` 化。
  - [ ] **P2-2a 組み込み関数と反復の helper**: `BuiltinFunc.Fn`、`src.Each` を経由する受け渡しのうち、参照や呼び出しだけを使う helper の引数を絞る。完了条件は既存の builtin・反復テストと、受け渡しだけの `VMCaller` 引数の削減。
  - [ ] **P2-2b host コールバックの helper**: `scriptIface.vc`、`bindCaller`、`callbackContext` から始まる受け渡しを絞る。P2-4 の capability を使い、登録時の process への束縛、再入、解放後の呼び出しを回帰テストで固定する。
- [x] **P2-3 使っていない `VMCaller` 引数を削る**: 対象は 96 関数。完了条件: スキャナ（[references/vmcaller.md](./references/vmcaller.md)）の「未使用」が 0 になる。
- [x] **P2-4 `vmOf` のダウンキャストをなくす**: 7 箇所を capability のメソッドに置き換える。
- [x] **P2-5 `internal/process` を抽出する**: goroutine、park/wake、デッドロック検出を移す。ループとのやり取りは `spawn`/`park`/`unpark` だけにする。完了条件: `make test-race` と `make concurrency-stress` が通る。
- [-] **P2-6 `special` を公開パッケージとして切り出す**: `SymbolID`、`QuotedCall`、`SpecialFunc`、`SpecialContext` を移し、`runtime` には型エイリアスを残す（I7）。レジストリと VM 側の context 実装は `internal/execution` に移す。完了条件: examples が無修正でコンパイルできる。
  - [x] `SymbolID` / `QuotedCall` → `special/` パッケージへ移動、`runtime` にエイリアス（PR #1141）
  - [ ] `SpecialFunc` / `SpecialContext` は `runtime.Value`/`Package`/`TypeDef` 参照のため移すと import cycle — `runtime` に残置。registry・context 実装の `internal/execution` 移行は P2-14 に含める
- [-] **P2-7 `internal/hostbridge` を抽出する**: `goValueOf`、`toReflectValueDepth`、`hostMarshal`、`adaptFunc`、`hostArgAs`、opaque store、host↔script の変換層（`hostErrTwins`、`goNative`、プロキシのボックス）を移す。完了条件: archcheck が `vm/` での `reflect` を禁止している。
  - [x] opaque コンテナの引数パッキング（`OpaqueStoreArg`/`opaqueStoreKey`/`opaqueStoreValue`/`namedStoreValue`/`OpaqueKeyPosition`/`NamedLoadValue`）を `internal/hostbridge` へ（PR #1142）
  - [ ] **P2-7a host 値との基本変換**: `goValueOf`、`goNative` と、値のコピー・box 化を担う処理を移す。名前付きスカラー、型付き nil、共有されるポインタ・map・slice の挙動を固定する。
  - [ ] **P2-7b 引数・返り値の変換**: P2-7a の変換を使って `toReflectValueDepth`、`hostMarshal`、`hostArgAs`、`hostErrTwins` を移す。可変長引数、入れ子のコレクション、書き戻し、error の回帰テストを通す。
  - [ ] **P2-7c 関数とインタフェースのアダプタ**: P2-2b と P2-7b のあとに `adaptFunc`、script interface のアダプタと proxy の box 化を移す。callback の再入、process 解放、panic の伝播を確認する。
  - [ ] **P2-7d VM の値操作に残る reflect**: `composite`、`convert`、`binop` の host 値操作を機能ごとに bridge 経由へ移す。各差分を該当する difffuzz と既存の host 値テストで確認する。
  - [ ] **P2-7e VM の channel 操作と依存制約**: `select` などに残る host channel 操作を切り替える。送受信、close、待機解除のテストを通し、残存する `reflect` import がゼロになった時点で `archcheck` に禁止規則と負例を加える。
- [x] **P2-8 `scriptIface` のアダプタ一族を生成に切り替える**: インタフェース → メソッド → arity の表から生成し、`scriptFileSeek` などの組み合わせ爆発をなくす。
- [ ] **P2-9 `builtin/` を抽出する**: `builtins.go` を移す。
  - [ ] **P2-9a 定数と値の共通 helper**: `uconstNative`、`constAtWidth`、`makeHostType` などを、root や Engine への依存がない置き場所に移す。定数の幅・オーバーフロー、名前付き host 型を既存テストで確認する。
  - [ ] **P2-9b 表示と callback の共通 helper**: `display` など実行側に問い合わせる helper を、必要な capability で呼べるようにする。型・値の表示と script method への再入を固定する。
  - [ ] **P2-9c builtin の登録を移す**: P2-9a/b のあとに `builtins.go` の組み込み関数と登録を移す。root の登録窓口から既存名を解決でき、examples が無修正で通ることを確認する。
- [x] **P2-10 stdlib: 名前レジストリと `bind.Env`**: テーブルはまだ即時に構築したまま、`BoundPackages`/`BoundSymbols`/`BoundOf`/`vet`/`SourceOf`/`NewSession` の優先順位/bound-shadow チェックを `names` ビューに移す。エンジンを捕捉している約 45 個のクロージャを `bind.Env` 経由にする。完了条件: 振る舞いが変わっていない。[references/stdlib-lazy.md](./references/stdlib-lazy.md)
  - [x] `bind.Env` インタフェース（`internal/bind`）を導入し、エンジン捕捉のクロージャを `h.env` 経由に変更（`bindenv.go` アダプタ）（PR #1144）
  - [x] 残り: `names` ビューへの移行（`BoundPackages`/`BoundSymbols`/`BoundOf`/`vet`/`SourceOf`/`NewSession` の優先順位・bound-shadow チェック）（PR #1145）
- [x] **P2-11 stdlib: `hostTwin` を install の入口につなぐ**: P2-10 の名前レジストリを使い、登録済みの stdlib パスについて、typedef を検索する前に対象パッケージの install を保証する入口を通す。この段階では起動時の即時構築を続ける。未登録のパスは従来どおり既存パッケージだけを検索する。完了条件: `time` を import しないスクリプトに別のパッケージから `time.Time`（例 `ctx.Deadline()` の戻り値）を渡し、対応する typedef を使えることをテストで固定する。（PR #1146）[references/stdlib-lazy.md](./references/stdlib-lazy.md)
- [-] **P2-12 stdlib: パッケージ単位の遅延構築**: P2-11 の入口で、`internal/stdlib/<path>` ごとの `Install(r, env)` を初回利用時に実行する。グローバルな `templateFuncMap` もここでレジストリ単位にする（バグとしてはルートの [TODO.md](../../../TODO.md) で管理している）。完了条件: P2-11 の回帰テストが通り、`time` のテーブルが初回の `hostTwin` 検索で構築されることを確認する。`NewSession` のベンチで差分を報告する（ベースラインでは install が実行時間の 60〜70%）。
  - [x] 遅延構築の仕組み（`bind.Pending` + installbound.go の入口、`templateFuncMap` のセッション化、ベンチ差分 0.47ms→10µs）: PR #1147
  - [ ] **P2-12a 初回 install と明示 Bind の登録順を固定する**: `html` の既定テーブルの初回構築中に、同じパスへ明示 `Bind` が完了しても、既定テーブルで上書きされる。公開時に明示登録を優先し、`WithHostPolicy` で順序を固定した並行テストで `BoundSymbols` と通常の import が明示テーブルを返すことを確かめる。 [登録処理](https://github.com/podhmo/minigo/blob/9918230e/installbound.go#L53-L65)
  - [ ] **P2-12b 依存の少ない stdlib installer を移す**: P2-9a のあとの最初の対象を `html` や `unicode/utf8` とし、`internal/stdlib/<path>` の `Install` から登録する。生成テーブルの再生成、初回構築、bound/source の結果を確認する。
  - [ ] **P2-12c 状態を持つ stdlib installer を移す**: P2-12a/b のあと、`bind.Env` を介して `time`・`context`、`text/template`・`html/template` など依存が近いパッケージを一組ずつ移す。間接の `time.Time`、Session ごとの `FuncMap`、source 優先設定を保ち、Session 生成と初回利用のベンチ差分を記録する。
- [x] **P2-13 純粋なシグネチャの bind を生成する**: 約半分のエントリを `gen-intrinsics` で生成し、`// Code generated` ヘッダ付きでチェックインする。完了条件: 生成したパッケージに、source 解釈版との parity テストがある。[references/stdlib-lazy.md](./references/stdlib-lazy.md) — `internal/cmd/genbind` が index の Funcs/Vars/Consts/Types から `<camel>GenEntries()` を生成（`wrapBind` 経由）。12 パッケージ・約 500 エントリを生成済み。環境依存の filepath 系・値コンストラクタ型・コールバック系は手書きのまま installX がマージする。parity テストは `genbind_parity_test.go`（構造: 全 export をカバー / 振る舞い: bound vs source の呼び出し結果一致）
- [-] **P2-14 `internal/execution` と `internal/source` を抽出する**: ライフサイクルの状態機械を `runtime/package.go` と `minigo.go` から移す。`embed.go`、`parse.go`、`parsecache.go` は `internal/source` へ移す。完了条件: root パッケージ全体（非テスト・非生成 .go の合計、現状約 19,500 行）と `vm.go` がそれぞれ約 3,000 行を下回る（`minigo.go` 単体は既に達成済みのため、測る範囲はパッケージ全体とする）。
  - [x] `parse.go`/`parsecache.go`/`embed.go` を `internal/source` へ（`ParseCache`/`IndexCache` + `ParsePackageFiles` + `EmbedBuiltin`/`EmbedEnv`）（PR #1149）
  - [x] **P2-14a root の完了条件を明確にする**: 実測: `minigo.go` 単体 1,489 行、root 直下の非テスト・非生成 Go の合計 19,338 行（archcheck の計測、`vm.go` は 883 行）。計画書の約 3,000 行は root パッケージ合計と解釈する（`minigo.go` 単体は達成済みのため）。残すファサードの責務: 公開 API（`Engine`/`NewEngine`/`Run`/`Call`/`Bind`/`NewSession`/`With*` オプション）とセッション状態の保持、内部実装は `internal/*` 側へ。
  - [x] **P2-14b index 完了の公開と待機を抽出する**: `buildPackage` と `indexFiles` の完了通知を抽出し、`runtime.Package` に互換の入口を残す。並行した初回ロード、strict/lenient の分離、パース・index 失敗後の待機解除と既存診断をテストする。→ `execution.IndexGate`（`internal/execution`）に抽出、`Package.MarkIndexed`/`FinishIndexing`/`WaitIndexed`/`IndexReady` が互換の窓口（#1155） — 完了
  - [ ] **P2-14c 初期化の状態遷移と runner を抽出する**: P2-14b のあとに `EnsureReady` / `EnsureReadyRun` が使う状態遷移・初期化処理を抽出する。一度だけの初期化、エラーと panic 後の状態、実行中の process に所属する初期化をテストする。→ `execution.Init` に一度だけの実行+panic→error 記録を抽出（#1156）
  - [-] **P2-14d 特殊形式の registry と context を抽出する**: registry は `execution.Registry[K,V]` に抽出済み（PR TBD）。context 実装（`vm/special.go` の `specialCtx`）は `*VM`/`*frame` 参照のため `internal/execution` へは移せない（execution は runtime を import できず、vm は外側）— vm 残留が正しい配置と判断。
  - [x] **P2-14e Session 所有権チェックの入口をそろえる**: 決定 — 外部から `*runtime.Package` を受けて自エンジンの実行で init/評価する入口は `Call` と `EvalExpr` の2つ。両方で `Owner != nil && Owner != e` を拒否。対象は source-built パッケージのみ（`buildPackage` が Owner=e を付与）で、host bind は Owner なしのまま Session 間共有を許す。`EvalExpr` にチェックを追加し、外国パッケージ拒否・bound 共有の2テストを追加（PR TBD）

## M4 + P3: 型情報の統合

- [ ] **M4-1 フィールドと埋め込み**: `semantics.FieldPath` を作り、`(TypeID, name)` ごとにパスをキャッシュする。完了条件: 「selector resolution re-searches by name」の TODO が解消する。[references/typeid.md](./references/typeid.md)
  - [x] **M4-1a 埋め込みを含むパス探索**: 宣言情報から `FieldPath` を求める。直下のフィールドの優先、同じ深さでの曖昧性、非公開フィールド、埋め込みポインタをテストする。→ `semantics.FieldPath`（`{Index, PkgPath, Exported}`、reflect の `Field.Index` 流の index 列）と `Service.FieldPathOf` を追加。埋め込みフィールドは型名でも選べ、曖昧性は Failed、非公開は PkgPath+Exported で呼び出し側が判定（PR TBD）
  - [x] **M4-1b selector の読み出し**: M4-1a のパスを VM の読み出しに使い、nil の埋め込みポインタや名前の衝突で既存の結果になることを確認する。
  - [x] **M4-1c 書き込みとアドレス取得**: M4-1b のあと、代入・複合代入・`&x.f` を同じパスに切り替える。書き込み先・アドレスの同一性・nil の診断を検証する。
  - [x] **M4-1d FieldPath のキャッシュ**: M4-1b/c と M4-3a のあと、`(TypeID, name)` ごとにパスを再利用する。宣言が変われば古いパスを使わず、selector が同じパスを再探索しないことを確認する。
- [ ] **M4-2 メソッドセットとインタフェース充足**: 型ごとに一度だけ計算する。`MethodSetOf`、`MethodFuncsOf`、充足判定、promote されたメンバの検索を、これに切り替える。完了条件: 「walks the method set twice」の TODO が解消する。
  - [x] **M4-2a メソッド集合の計算**: 値・ポインタレシーバ、深さ、曖昧な埋め込みを宣言情報から計算し、`MethodSetOf` / `MethodFuncsOf` が同じ結果を使うようにする。
  - [x] **M4-2b インタフェース充足判定**: M4-2a の集合を型アサーションと充足判定から使う。値・ポインタの差、同名別パッケージ、シグネチャ不一致の診断を固定する。
  - [x] **M4-2c promoted method の検索**: M4-2a の結果をメソッド値と呼び出しに使い、レシーバ調整と曖昧性を既存テストで照合する。重複探索がなくなったことを確認する。
- [ ] **M4-3 TypeID と Session ごとの intern 表**: `execution` に `TypeID → *runtime.TypeDef` の表を置き、`hostTypedefOf`、`hostPkgElemTypedef`、`HostTypeOf`、インスタンス化によるコピーをすべてこの表経由にする。host 型のキーは表している型の (PkgPath, Name, 型引数) とする。完了条件: `atomic.Value` と `atomic.Pointer` が別の型として扱われる pin 済みケースがある。[references/typeid.md](./references/typeid.md)
  - [x] **M4-3a TypeID のキーと反例**: source named 型、host が表す型、無名型のキーを定める。`atomic.Value` と `atomic.Pointer` の区別、運搬型が異なる `*time.Timer` の同一性、ソース版と型引数の違いを固定する。M4-1d のキャッシュはこのキーを使う。
  - [x] **M4-3b Session 内の intern 表**: M4-3a と P2-14c のあとに導入する。同じキーを並行登録しても Session 内で同じ typedef を返し、別 Session の typedef と混ざらないことをテストする。
  - [x] **M4-3c host typedef の入口を切り替える**: `hostTypedefOf` / `hostPkgElemTypedef`、`HostTypeOf` / bind 側を入口ごとに移す。source・bind 間の twin、型付き nil と型の表示を各差分で確認する。
  - [x] **M4-3d インスタンス化の typedef を切り替える**: M4-3b の表へコピーを集約する。型引数が異なる型、別宣言の同名型、別 Session の分離を確認する。
- [ ] **M4-4 host twin 系の helper を消す（1 つにつき 1 PR）**: 対象は `hostTwinIs`、`hostNilTwin`、`sameSpelledTwin`、`HostNamedIs`/`HostTwinOf`、`sourceTwinOf`。完了条件: 各 PR が pin 済みのリグレッションケースで裏付けられている。
- [ ] **M4-5 代入可能性と変換可能性を semantics に移す**: `tdShapeEq`、`underlyingShape`/`convShapeEq`、`structFieldsEq` を、`AssignableTo`/`ConvertibleTo`/`Identical` として移す（消すのではない）。`peelAlias` は `TypeOf` の解決に含める。
  - [x] **M4-5a 同一性とエイリアス**: `Identical` と `TypeOf` の alias 解決を先に導入する。定義型・エイリアス、構造体のタグ、パッケージ固有名の比較を固定する。
  - [x] **M4-5b 代入可能性**: M4-5a の結果を使って `AssignableTo` を移す。型なし定数、named/unnamed の組み合わせ、インタフェース充足を既存挙動と照合する。
  - [x] **M4-5c 変換可能性**: `ConvertibleTo` を移し、構造・タグや数値など、代入の規則と結果が異なるケースを分けて検証する。
- [ ] **M4-6 ジェネリクス推論を分割する**: TypeID の上での単一化は `semantics` に、引数の値から TypeID への対応づけは実行側のアダプタに置く。インスタンスキャッシュのキーは `(関数宣言, []TypeID)` にする。完了条件: 「per-call key spelling」の TODO が解消する。
  - [x] **M4-6a TypeID 上の単一化**: M4-3 のキーを使い、値の取り出しを実行側アダプタに残して推論を移す。制約、部分指定、推論不能の診断を固定する。
  - [x] **M4-6b インスタンスキャッシュのキー**: M4-6a のあとに `(関数宣言, []TypeID)` へ切り替える。正しいキャッシュ再利用と衝突がないことを確認し、既存のジェネリクスベンチで差分を記録する。
- [ ] **M4-7 遅延された事実を実装する**: M2-3 で事実と仕分けた問いについて、Deferred のときは `CallSite` に `TypeRef` を持たせ、VM が最初の実行時に `AllowIndex` で解決する。Missing と Failed は `TRAP` にする。完了条件: 15 件の受け入れセットが、強制的に Deferred にしても通る。[references/acceptance-set.md](./references/acceptance-set.md)
  - [x] **M4-7a 遅延参照を保持・解決する経路**: `CallSite` に必要な `TypeRef` とソース位置だけを載せる。VM での初回解決、Missing/Failed の `TRAP`、未実行分岐で解決が走らないことを固定する。既存の引数バッファ等の移行は P4-4 で扱う。
  - [x] **M4-7b import 名・型宣言・複合リテラル**: M4-7a を使い、`assign_pkgname_differs`、`litkey_named_peel`、`src_pkgtype_maplit_keys`、`TestSelMemberSyntheticTypeEntry` を強制 Deferred で通す。
  - [x] **M4-7c 定数の有無・型・シフト**: `isConstExprDepth`、`certainlyNonConstDepth` と修飾定数の判定を移す。`shift_const_count`、`gate_qualconst`、`gate_qualconstbind` を強制 Deferred で通す。
  - [x] **M4-7d 比較・変換・nil 判定と診断**: 残る selector 経路と診断用の package 名を移し、`TestNetHTTPRoundtrip`、残りの `gate_qual*`、`selconv_typearg`、`selnilable_trap` を固定する。全 15 件で通常・強制 Deferred の出力または `TRAP` 文言を照合する。
- [ ] **M4-8 `WithStrictLazyCompile` を入れて CI で回す**: `IndexedOnly` で、すべての事実を Deferred にしてコンパイルするモード。完了条件: conformance スイートの結果がこのモードでも同一である。
  - [x] **M4-8a strict モードとロード順の比較**: M4-7 の全ケースが通ってから `WithStrictLazyCompile` を導入する。未 index と事前 index の両条件で結果を比較し、C1 の計測でコンパイル時初期化がゼロであることを確かめる。
  - [x] **M4-8b CI の conformance ジョブ**: M4-8a を通常モードと併走させる。15 件の受け入れセットに加え、既存 conformance の出力と `TRAP` の同値性を確認する。
- [ ] **M4-9 `staticOpTyp` 系の裏ルートを置き換える**: `staticOpTyp`/`callOpTyp`/`opTyp`/`stampVarTyps`/`OpLocalTyp`/`OpGlobalTyp`/`statics` を、chunk に格納するヒントと事実に置き換える。
  - [x] **M4-9a 既存の静的情報を仕分ける**: `staticOpTyp` / `callOpTyp` / `opTyp` の利用側を列挙し、変換・推論・診断で事実を必要とするケースを固定する。[references/static-typ-sites.md](./references/static-typ-sites.md)
  - [ ] **M4-9b chunk への格納と旧経路の削除**: M4-7/8 のあと、`stampVarTyps`、`OpLocalTyp`、`OpGlobalTyp`、`statics` を利用側ごとに置き換える。各差分で通常・strict のテストと bytecode golden を照合する。

## M5: PackageImage と PackageInstance

- [ ] **M5-1 `PackageImage` を具体化する**: 不変で共有できる定義（AST、index、宣言位置、import spec、言語バージョン、ビルド設定）として切り出す。完了条件: archcheck が、イメージの中に実行状態が入らないことを保証している。[references/ownership.md](./references/ownership.md)
  - [x] **M5-1a 不変の定義を組み立てる**: P2-14b の index 結果から AST・index・宣言位置・import spec をまとめる。共有資産に実行時の `ImportRef` や cell が入る負例を `archcheck` で検出する。
  - [x] **M5-1b ソースのロード経路を切り替える**: M5-1a に言語バージョンとビルド設定を持たせ、通常ロードと inspect 用の lenient ロードから参照する。両経路の宣言・診断と遅延初期化の挙動を照合する。
- [ ] **M5-2 `PackageInstance` を具体化する**: globals、初期化状態、実行時の `ImportRef`、materialize した関数と型を持たせる。`runtime.Package` は互換の窓口として残す。完了条件: 同じイメージから作った 2 つのインスタンスが独立している。
  - [x] **M5-2a Session ごとの globals と初期化状態**: P2-14c と M5-1 のあと、共有イメージからそれぞれの実行状態を作る。同じソースの 2 つの Session で globals が独立し、初期化がそれぞれ一度だけ走ることを確認する。
  - [x] **M5-2b import と materialize の所有**: M5-2a のあと、実行時 `ImportRef` と関数・型の materialize キャッシュを各インスタンスに移す。並行した最初の参照、別 Session への漏出、既存 `runtime.Package` API との互換性を検証する。
- [ ] **M5-3 ソース版と派生キャッシュの所有**: キャッシュのキーに内容、ファイル集合、言語バージョン、ビルド設定を含める。キャッシュから追い出しても、既存の Session の参照は変わらない。
  - [x] **M5-3a ソース版とキャッシュキー**: ファイル内容・集合、言語バージョン、ビルド設定を変えたときに新しい版が使われることを個別に固定する。既存 Session が参照する版は編集後も保持する。
  - [x] **M5-3b 派生キャッシュの所有と eviction**: M5-2 と M5-3a のあと、parse/index と Session 状態の再利用単位を明記する。追い出し後に旧 Session の値と位置情報が保たれ、新 Session が新版を読み込むことをテストする。
- [ ] **M5-4 配列長の畳み込みの所有を明示する**: `runtime.arrayFolds`（グローバル、weak pointer で管理）を、どの所有単位に属するかを決めて移す。
  - [x] **M5-4a 配列長の依存と再 parse の理由を固定する**: 別ファイルの定数を編集したときの旧・新 Session の配列長と型情報、並行参照時の挙動をテストする。
  - [x] **M5-4b 畳み込みの所有を移す**: M5-3 と M5-4a のあと、派生情報をその結果が属する版またはインスタンスへ移す。再 parse の特例は、外しても M5-4a が通ることを確認できた場合に限り外す。

## P4: VM の形

- [ ] **P4-1 bytecode verifier と `MaxStack`**: スタック効果、ジャンプ先、スロット種別を検査する。テスト時とデバッグオプション指定時に走らせる。
  - [x] **P4-1a 命令と制御フローの検証**: スタック効果、分岐先、マージ地点の深さを検証し `MaxStack` を計算する。既存 golden と、不正な分岐・スタック不足の負例を使う。
  - [x] **P4-1b スロット種別と検証の入口**: P4-1a にスロット種別の検査を足し、テストとデバッグオプションから有効にする。正常な chunk の結果と診断が保たれることを確認する。
- [x] **P4-2 スロット分類**: ストア、selector の読み出し、レシーバを分類の対象にする。Plain なローカルでは `OpNewLocal`/`OpRenewVar` がその場に書き込む。特殊形式の呼び出し地点から見えるローカルは Boxed にする。完了条件: escape する chunk が約 10% になる。[references/slot-classification.md](./references/slot-classification.md)
  - [x] **P4-2a 分類を記録する**: ストア、読み出し、アドレス取得、捕捉、メソッドのレシーバ、特殊形式からの参照を分類して計測する。M4 の事実を使い、レシーバが不明な場合は保守的に Boxed とする。
  - [x] **P4-2b Plain ローカルの初期化と書き込み**: P4-2a と P4-1 のあと、`OpNewLocal` / `OpRenewVar` を Plain スロットの直接書き込みに切り替える。ループ変数・多重代入・名前付き戻り値を確認する。
  - [x] **P4-2c selector・レシーバと特殊形式**: P4-2b のあと、値の読み出しとアドレスが必要な経路を分ける。ポインタレシーバ、closure、Quoted Go が参照するローカルの同一性を確認し、escape 率を同じケース集合で測る。
- [x] **P4-3 goroutine ごとの連続スタック**: frame を `(base, sp)` の窓にし、引数はその場で渡す。
  - [x] **P4-3a frame を連続スタックの窓にする**: P4-1/2 のあと、goroutine ごとに `(base, sp)` を使う。ネストした呼び出し、スタックの拡張、panic/recover 前後の値をテストする。
  - [x] **P4-3b 引数をスタック上で受け渡す**: P4-3a のあと、通常呼び出しから切り替える。可変長引数、defer、host callback を個別に移し、既存の結果と割り当て数を測る。
- [ ] **P4-4 `CallSite` 記述子**: 引数のヒント、spread の型、quote インデックス、遅延された事実、ソース位置をまとめる。完了条件: `frame.argbuf/statbuf` と `deferredCall.statics/spreadTd` が消えている。
  - [ ] **P4-4a 通常呼び出しの引数メタデータ**: M4-7a の `CallSite` に引数のヒント、spread の型、quote インデックスを載せる。通常・可変長・特殊形式で結果が一致することを確認する。
  - [ ] **P4-4b defer とバッファの撤去**: P4-4a のあと、defer が同じ記述子を使うようにする。評価順、spread、panic 経由の呼び出しをテストし、既存のバッファと重複フィールドを削除する。
- [ ] **P4-5 インラインキャッシュ**: `OpGlobal`（`Env` epoch でガード）と `OpSelect`（`TypeID → パス`）にキャッシュを置く。Chunk は Session ごとのままにする。完了条件: `make test-race` と `make concurrency-stress` が通る。
  - [ ] **P4-5a OpGlobal のキャッシュ**: `Env` epoch で再束縛時に無効化する。別 Session や同一 Session 内の goroutine で値が混ざらないことを確認し、ベンチ差分を記録する。
  - [ ] **P4-5b OpSelect のキャッシュ**: M4-1d と M4-3 のあと、`TypeID` ごとの `FieldPath` を使う。同じサイトに異なるレシーバ型が来る場合と、並行アクセスをテストする。
- [ ] **P4-6 `OpEvalAST` を `OpEvalFragment` に改名する**: REPL/`EvalExpr` のブリッジとしてだけ残す。独立した PR で命令名・逆アセンブル出力・golden を更新し、REPL と `EvalExpr` の挙動を確認する。

## M6（任意・判断ゲート付き）

- [ ] **M6-1 コンパイル済みコードの共有を検討する**: Chunk が Session に依存する参照を分離できるかどうかを判断し、記録する。
- [ ] **M6-2 Session 間での bind テーブルの共有を検討する**: `bind.Env` の状態を呼び出しコンテキストへ移せるかどうかを判断する。遅延 install のあとでもセッションの起動コストが大きい場合に限る。
- [ ] **M6-3 シンボル単位の materialize を検討する**: realworld で parse/index が支配的な場合に限る。
- [ ] **M6-4 tagged `Value` を検討する**: P4 のあとも boxing が単独で約 15% を超える場合に限る。既定は「やらない」。
