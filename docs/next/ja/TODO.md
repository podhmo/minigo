# TODO: リアーキテクチャ（次期）

> リポジトリ直下の [TODO.md](../../../TODO.md) とは別に、[リアーキテクチャ計画](../../sketch/ja/plan-rearchitecture.md)（第 3 版）を実行可能な単位に分けたもの。運用は [todo-md スキル](../../../.claude/skills/todo-md/SKILL.md) に準じる。`[ ]` は未着手、`[-]` は一部完了、`[x]` は完了を表す。
>
> - 各項目は 1 PR 程度の大きさにした。番号はおおむね依存の順になっている。
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

- [x] **M3-1 軽量な所有権チェック**: 他の Session の PackageInstance を、初期化や呼び出しの前に拒否する（開発時のみ有効）。完了条件: 誤用テスト（Session A の dep を Session B の Call に渡す）がある。[references/ownership.md](./references/ownership.md)
- [x] **M3-2 資産監査をオプトインで分ける**: AST/index の改変を検出する。D-own の計測ではリロード+呼び出しが約 2〜3 ms、完全ハッシュ付きでは約 1〜1.6 s なので、既定では無効にする。完了条件: 通常の実行ではハッシュのコストを払っていない。

## P2: パッケージの抽出

- [x] **P2-1 capability インタフェースを定義する**: `VMCaller` を union として残す。隠れた capability（`HostClear`、`HostMapDelete`、`HostNilTwin`、`MaterializeConstErr`、`CallCatching`）も含める。完了条件: 型アサーションで呼んでいた箇所が、インタフェースのメソッド呼び出しになっている。[references/vmcaller.md](./references/vmcaller.md)
- [-] **P2-2 受け渡し先の helper を capability 型にする**: `runtime.IfaceMember`、`hostErrOf`、`display`、`callOrPanic`、`goJSONSeen` などが対象。完了条件: caller を受け渡すだけの 184 関数が、本当に必要とする capability に絞られている。
  - [x] JSON/io/errors/maps/seq 系の中核 helper を絞った（`IfaceMember`→`MemberLookup`、`hostErrOf`→`MemberCaller`、`goJSONSeen`→`MemberShapeCaller`、`callOrPanic`→`ScriptCaller` など）。
  - [x] メソッド別の原子 capability（`ValTyper`/`Converter`/`Equaler`/`ElemZeroer`/`ArraySizer`/`MapKeyer`/`NewMapper`/`Copier`）と合成型を追加し、中間層の受け渡し関数を絞った。`fmtValue.c`/`scriptLocker.vc` フィールドも `MemberCaller` 化。
  - [ ] 残り。`BuiltinFunc.Fn`/`src.Each`/`scriptIface.vc`/`bindCaller`/`callbackContext`/`vmOf` など VMCaller 型のフィールド・ダウンキャストにアンカーされている関数（bf() 登録群・ffi marshaling 群の大半）は P2-4 の前提になる。
- [x] **P2-3 使っていない `VMCaller` 引数を削る**: 対象は 96 関数。完了条件: スキャナ（[references/vmcaller.md](./references/vmcaller.md)）の「未使用」が 0 になる。
- [x] **P2-4 `vmOf` のダウンキャストをなくす**: 7 箇所を capability のメソッドに置き換える。
- [x] **P2-5 `internal/process` を抽出する**: goroutine、park/wake、デッドロック検出を移す。ループとのやり取りは `spawn`/`park`/`unpark` だけにする。完了条件: `make test-race` と `make concurrency-stress` が通る。
- [-] **P2-6 `special` を公開パッケージとして切り出す**: `SymbolID`、`QuotedCall`、`SpecialFunc`、`SpecialContext` を移し、`runtime` には型エイリアスを残す（I7）。レジストリと VM 側の context 実装は `internal/execution` に移す。完了条件: examples が無修正でコンパイルできる。
  - [x] `SymbolID` / `QuotedCall` → `special/` パッケージへ移動、`runtime` にエイリアス（PR #1141）
  - [ ] `SpecialFunc` / `SpecialContext` は `runtime.Value`/`Package`/`TypeDef` 参照のため移すと import cycle — `runtime` に残置。registry・context 実装の `internal/execution` 移行は P2-14 に含める
- [ ] **P2-7 `internal/hostbridge` を抽出する**: `goValueOf`、`toReflectValueDepth`、`hostMarshal`、`adaptFunc`、`hostArgAs`、opaque store、host↔script の変換層（`hostErrTwins`、`goNative`、プロキシのボックス）を移す。完了条件: archcheck が `vm/` での `reflect` を禁止している。
- [ ] **P2-8 `scriptIface` のアダプタ一族を生成に切り替える**: インタフェース → メソッド → arity の表から生成し、`scriptFileSeek` などの組み合わせ爆発をなくす。
- [ ] **P2-9 `builtin/` を抽出する**: `builtins.go` を移す。
- [ ] **P2-10 stdlib: 名前レジストリと `bind.Env`**: テーブルはまだ即時に構築したまま、`BoundPackages`/`BoundSymbols`/`BoundOf`/`vet`/`SourceOf`/`NewSession` の優先順位/bound-shadow チェックを `names` ビューに移す。エンジンを捕捉している約 45 個のクロージャを `bind.Env` 経由にする。完了条件: 振る舞いが変わっていない。[references/stdlib-lazy.md](./references/stdlib-lazy.md)
- [ ] **P2-11 stdlib: `hostTwin` の検索で install を起動する**: 他のパッケージから流れてきた host 値（`ctx.Deadline()` → `time.Time`）が bind 済みの typedef を見つけられるようにする。完了条件: `time` を import しないスクリプトでのテストがある。
- [ ] **P2-12 stdlib: パッケージ単位の遅延構築**: `internal/stdlib/<path>` ごとに `Install(r, env)` を持たせ、初めて必要になったときに構築する。グローバルな `templateFuncMap` もここでレジストリ単位にする（バグとしてはルートの [TODO.md](../../../TODO.md) で管理している）。完了条件: `NewSession` のベンチで差分を報告している（ベースラインでは実行時間の 60〜70%）。
- [ ] **P2-13 純粋なシグネチャの bind を生成する**: 約半分のエントリを `gen-intrinsics` で生成し、`// Code generated` ヘッダ付きでチェックインする。完了条件: 生成したパッケージに、source 解釈版との parity テストがある。[references/stdlib-lazy.md](./references/stdlib-lazy.md)
- [ ] **P2-14 `internal/execution` と `internal/source` を抽出する**: ライフサイクルの状態機械を `runtime/package.go` と `minigo.go` から移す。`embed.go`、`parse.go`、`parsecache.go` は `internal/source` へ移す。完了条件: root と `vm.go` がそれぞれ約 3,000 行を下回る。

## M4 + P3: 型情報の統合（機能グループごとに 1 PR）

- [ ] **M4-1 フィールドと埋め込み**: `semantics.FieldPath` を作り、`(TypeID, name)` ごとにパスをキャッシュする。完了条件: 「selector resolution re-searches by name」の TODO が解消する。[references/typeid.md](./references/typeid.md)
- [ ] **M4-2 メソッドセットとインタフェース充足**: 型ごとに一度だけ計算する。`MethodSetOf`、`MethodFuncsOf`、充足判定、promote されたメンバの検索を、これに切り替える。完了条件: 「walks the method set twice」の TODO が解消する。
- [ ] **M4-3 TypeID と Session ごとの intern 表**: `execution` に `TypeID → *runtime.TypeDef` の表を置き、`hostTypedefOf`、`hostPkgElemTypedef`、`HostTypeOf`、インスタンス化によるコピーをすべてこの表経由にする。キーは表している型 (PkgPath, Name) とする。完了条件: `atomic.Value` と `atomic.Pointer` が別の型として扱われる pin 済みケースがある。[references/typeid.md](./references/typeid.md)
- [ ] **M4-4 host twin 系の helper を消す（1 つにつき 1 PR）**: 対象は `hostTwinIs`、`hostNilTwin`、`sameSpelledTwin`、`HostNamedIs`/`HostTwinOf`、`sourceTwinOf`。完了条件: 各 PR が pin 済みのリグレッションケースで裏付けられている。
- [ ] **M4-5 代入可能性と変換可能性を semantics に移す**: `tdShapeEq`、`underlyingShape`/`convShapeEq`、`structFieldsEq` を、`AssignableTo`/`ConvertibleTo`/`Identical` として移す（消すのではない）。`peelAlias` は `TypeOf` の解決に含める。
- [ ] **M4-6 ジェネリクス推論を分割する**: TypeID の上での単一化は `semantics` に、引数の値から TypeID への対応づけは実行側のアダプタに置く。インスタンスキャッシュのキーは `(関数宣言, []TypeID)` にする。完了条件: 「per-call key spelling」の TODO が解消する。
- [ ] **M4-7 遅延された事実を実装する**: M2-3 で事実と仕分けた問いについて、Deferred のときは `CallSite` に `TypeRef` を持たせ、VM が最初の実行時に `AllowIndex` で解決する。Missing と Failed は `TRAP` にする。完了条件: 15 件の受け入れセットが、強制的に Deferred にしても通る。[references/acceptance-set.md](./references/acceptance-set.md)
- [ ] **M4-8 `WithStrictLazyCompile` を入れて CI で回す**: `IndexedOnly` で、すべての事実を Deferred にしてコンパイルするモード。完了条件: conformance スイートの結果がこのモードでも同一である。
- [ ] **M4-9 `staticOpTyp` 系の裏ルートを置き換える**: `staticOpTyp`/`callOpTyp`/`opTyp`/`stampVarTyps`/`OpLocalTyp`/`OpGlobalTyp`/`statics` を、chunk に格納するヒントと事実に置き換える。

## M5: PackageImage と PackageInstance

- [ ] **M5-1 `PackageImage` を具体化する**: 不変で共有できる定義（AST、index、宣言位置、import spec、言語バージョン、ビルド設定）として切り出す。完了条件: archcheck が、イメージの中に実行状態が入らないことを保証している。[references/ownership.md](./references/ownership.md)
- [ ] **M5-2 `PackageInstance` を具体化する**: globals、初期化状態、実行時の `ImportRef`、materialize した関数と型を持たせる。`runtime.Package` は互換の窓口として残す。完了条件: 同じイメージから作った 2 つのインスタンスが独立している。
- [ ] **M5-3 ソース版と派生キャッシュの所有**: キャッシュのキーに内容、ファイル集合、言語バージョン、ビルド設定を含める。キャッシュから追い出しても、既存の Session の参照は変わらない。
- [ ] **M5-4 配列長の畳み込みの所有を明示する**: `runtime.arrayFolds`（グローバル、weak pointer で管理）を、どの所有単位に属するかを決めて移す。

## P4: VM の形

- [ ] **P4-1 bytecode verifier と `MaxStack`**: スタック効果、ジャンプ先、スロット種別を検査する。テスト時とデバッグオプション指定時に走らせる。
- [ ] **P4-2 スロット分類**: ストア、selector の読み出し、レシーバを分類の対象にする。Plain なローカルでは `OpNewLocal`/`OpRenewVar` がその場に書き込む。特殊形式の呼び出し地点から見えるローカルは Boxed にする。完了条件: escape する chunk が約 10% になる。[references/slot-classification.md](./references/slot-classification.md)
- [ ] **P4-3 goroutine ごとの連続スタック**: frame を `(base, sp)` の窓にし、引数はその場で渡す。
- [ ] **P4-4 `CallSite` 記述子**: 引数のヒント、spread の型、quote インデックス、遅延された事実、ソース位置をまとめる。完了条件: `frame.argbuf/statbuf` と `deferredCall.statics/spreadTd` が消えている。
- [ ] **P4-5 インラインキャッシュ**: `OpGlobal`（`Env` epoch でガード）と `OpSelect`（`TypeID → パス`）にキャッシュを置く。Chunk は Session ごとのままにする。完了条件: `make test-race` と `make concurrency-stress` が通る。
- [ ] **P4-6 `OpEvalAST` を `OpEvalFragment` に改名する**: REPL/`EvalExpr` のブリッジとしてだけ残す。

## M6（任意・判断ゲート付き）

- [ ] **M6-1 コンパイル済みコードの共有を検討する**: Chunk が Session に依存する参照を分離できるかどうかを判断し、記録する。
- [ ] **M6-2 Session 間での bind テーブルの共有を検討する**: `bind.Env` の状態を呼び出しコンテキストへ移せるかどうかを判断する。遅延 install のあとでもセッションの起動コストが大きい場合に限る。
- [ ] **M6-3 シンボル単位の materialize を検討する**: realworld で parse/index が支配的な場合に限る。
- [ ] **M6-4 tagged `Value` を検討する**: P4 のあとも boxing が単独で約 15% を超える場合に限る。既定は「やらない」。
