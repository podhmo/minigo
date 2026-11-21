# リアーキテクチャの残件

[実装 TODO](./TODO.md) と [第 3 版の計画](../../sketch/ja/plan-rearchitecture.md) を、2026-11-19 時点の `1686c579`（PR #1227 の先端）で実装・テスト・測定記録と照合した。マージベースは `faf5a073`。各項目は確認できた残件または完了判定に追加で必要な検証を示す。

## GitHub stacked PRs

監査の記録を [PR #1227](https://github.com/podhmo/minigo/pull/1227) の上に積む。後続 PR は、前の変更を必要とするものを下から順に積む。

1. **strict lazy の検証 → 既知の相違の修正**。まず独立した cold/warm コンパイルを検証し、次に select 受信・シフト・bound 定数比較を個別に直す。
2. **所有権の検査 → semantics の移設**。先に境界の負例を追加し、その境界で型比較と推論をそれぞれ移す。TypeID と chunk キャッシュの切り替えは別 PR にする。
3. **bytecode verifier → 呼び出しと fragment の整理**。生成経路ごとの verifier を先に有効にし、通常実行に残る fragment、spread・defer・host callback を個別に扱う。
4. **stdlib と root の縮小**。installer の parity と初回起動コストを確定し、依存の近い installer ごとに移す。公開 API の互換テストは各段階で回す。

ファイルサイズの検査、型だけの問い合わせ、メソッド集合の既知バグなど、前提が独立している修正は独立した PR にできる。各 PR では対応する TODO と、修正を確かめるテストまたは判断記録を更新する。

## 完了条件まで残っている実装

- [ ] **M0-4 / M4-8 — 型だけの問い合わせで init が走る経路を直す**。index の問い合わせと未実行分岐は遅延性を確認済み。`pkg.Member("T")` とスクリプト内の `var t depb.T` は、現在の pin テストが init の panic を期待している。型の解決には宣言の materialize だけを使い、値の読み出しで初期化する。通常・strict 両方で `Indexed` のままになり、値に触れた時点で初期化されることをパス単位で固定する。[pin テスト](../../../lazypin_test.go)・[既存のバグ票](../../../TODO.md)
- [ ] **M1-3 / M5-1 — 同じパッケージの実行状態型も検査する**。フィールド検査は修飾型名だけを禁止リストと照合する。合成した `runtime` パッケージで `PackageImage` に `Leaked *Cell` を置くと、違反が 0 件になることを再現した。同一パッケージの識別子も完全修飾名に正規化して照合し、無修飾の型・エイリアスの負例を足す。[名前の検査](../../../internal/archcheck/archcheck.go#L326-L355)
- [ ] **M1-4 — ファイルサイズの上限をベースブランチから単調にする**。`CheckSizes` は現在の行数と同じブランチ上の表を突き合わせ、両方を増やすと通過する。実際に `b5642958` で既存ファイルの予算が増加した。CI で PR のマージベースにある表と比較し、既存パスの増額を検出する。ファイル分割や明示的な例外の扱いも同時に記録する。[サイズ検査](../../../internal/archcheck/archcheck.go#L202-L226)・[予算の増額](https://github.com/podhmo/minigo/commit/b5642958b428a6f482629a28856da5adadf21626)
- [ ] **M2-2 / M2-4 / M5-1 — アーキテクチャ検査の実行入口と禁止境界をそろえる**。`go test` は `compile` の禁止呼び出しと `PackageImage` の共有フィールドを検査する。`make archcheck` の CLI は両方を省き、`internal/source` が `EnsureReady` 系を呼ぶ経路も禁止されていない。semantics の `Source.Load` はこのアダプターへ到達する。CI のテストと CLI で検査を共有し、source 側に初期化呼び出しを加えた負例が落ちるようにする。[CLI](../../../internal/cmd/archcheck/main.go#L59-L78)・[禁止規則](../../../internal/archcheck/rules.go#L56-L71)・[テスト](../../../internal/archcheck/archcheck_test.go#L100-L146)
- [ ] **M2-4 / M4-7 — Deferred のときに事実を読める経路を解消する**。`DeferFacts` は `AllowIndex` でロードしたパッケージを `Val` に保持したまま `Deferred` に変え、合成型エントリのテストは `selMember` がその値で答えることを期待する。未 index の strict 実行ではその値がない。Found/Missing/Deferred/Failed を型分類側まで伝え、合成型エントリの既 index と未 index を分けて、事実の実行時解決と TRAP を検証する。[問い合わせ](../../../compile/typehint.go#L2308-L2321)・[合成型のテスト](../../../compile/sel_member_test.go#L23-L60)
- [ ] **M4-2b — 非公開インタフェースメソッドの宣言パッケージを照合する**。要求とメソッド集合を名前だけで照合するので、別パッケージの同名 `m` が要求を満たし得る。非公開名はパッケージパスを含むキーにし、同一・別パッケージをそれぞれテストする。[要求集合](../../../dispatch.go#L994-L1039)・[既存のバグ票](../../../TODO.md)
- [ ] **M4-2c — host 構造体を基にした定義型の promoted method を拾う**。`type Wrap Base` で `Base` の匿名フィールド自身のメソッドが promoted されるケースが未対応。Go が引き継ぐ匿名フィールド由来の集合を、selector とインタフェース充足の両方で使う。深さ競合・値／ポインタレシーバも同じケースで確認する。[メソッド探索](../../../dispatch.go#L433-L584)・[既存のバグ票](../../../TODO.md)
- [ ] **M4-4 — host twin の後続作業に intern 前の照合も含める**。残る 2 helper の維持は今回の決定。将来の除去では `Engine.hostTwin` が正規化中に行う宣言検索と、VM が intern 後に使う fallback の両方を移す。無名ポインタの綴り照合は独立の経路なので、同じ除去で挙動が変わらないことを pin する。[決定記録](../../open-questions.md)・[正規化](../../../typeid.go#L137-L194)・[宣言検索](../../../minigo.go#L478-L494)・[VM 側](../../../vm/assert.go#L1240-L1292)・[旧設計表](./references/typeid.md#helper-の行き先)
- [ ] **M4-5 — 代入可能性・変換可能性・同一性の配置を決着させる**。VM から抽出した中核は `runtime.ShapeEq` と `runtime.ConvertibleTo` にある。計画どおりに進める場合は宣言情報を受け取る純粋な判定を `internal/semantics` へ移し、実行時 typedef の変換をアダプターに残す。現在の境界を採用する場合は計画と完了条件を更新し、各判定の利用側を記録する。[代入側](../../../runtime/assignable.go)・[変換側](../../../runtime/convertible.go)・[semantics の API](./references/semantics-api.md)
- [ ] **M4-6a — ジェネリクス単一化を型の問い合わせとして分ける**。`runtime.Inferer` への抽出は済んだが、入力と束縛の表現は `*TypeDef` と `map[string]Value` のまま。宣言と型 ID を入力にする単一化を semantics に置き、値からの型抽出は VM に置く。現状を採用する場合も計画上の K6 と完了条件をそろえる。[単一化](../../../runtime/infer.go#L11-L59)・[K6](../../sketch/ja/plan-rearchitecture.md#8-判断の記録)
- [ ] **M4-6b — コンパイル済み generic chunk のキーを移す**。推論・明示インスタンスのキャッシュは TypeID を使う。コンパイル済み chunk の `instM` は引き続き `runtime.InstKey` でソース位置や束縛の型の綴りを組み立てる。こちらも宣言と順序付き TypeID をキーにするか、綴りを残す理由を記録する。既存の推論キー切替と同様、複合型・別宣言・型 ID が取れない場合を固定して測る。[コンパイル時の利用](../../../compile/compile.go#L152-L171)・[キー](../../../runtime/package.go#L514-L552)・[移行の記録](https://github.com/podhmo/minigo/commit/26cd5480010f3e26ef4040c80a98890a006593f0)
- [-] **M4-8b / M4-9b — strict の差分と旧命令を追跡する**。既知差分はすべて解消済み。旧 `stampVarTyps`、`OpLocalTyp`、`OpGlobalTyp` はセルにある宣言型を読むため維持する設計が記録されている。残す経路と chunk に移した事実を TODO の完了条件にも反映する。[strict ハーネス](../../../difffuzz_test.go#L49-L66)・[bind-site の判断記録](./references/static-typ-sites.md)
  - [x] **`gate_recvdeferred`** — Resolve the inferred channel element type in the declaring package and pin the select receive diagnostic in normal and strict modes.
  - [x] **`text_pass_constdom`** — Keep the deferred shift count's `UConst` box in the `$shift` scratch local (`OpNewLocal` B!=0) so a bound member count such as `math.MaxUint` stays a uint64 count instead of materializing to int.
  - [x] **`time_weekday_bound`** — Resolve a `.imp:`-prefixed qualifier in `importPackageAt` by loading the path its suffix encodes (the `preparedDivergent` pins for `gate_qualforeign{,_case,_mon,_nil}` came off with the same fix).
- [ ] **P4-1b — すべての生成経路で verifier を通す**。`WithBytecodeVerify` のチェックは `Engine.compileFunc` が作る外側の chunk に掛かる。入れ子の関数リテラル、パッケージの変数／定数初期化、式のコンパイルは別経路。これらを同じ verifier に通し、各経路について不正な chunk を実行前に拒否するテストを足す。[検証入口](../../../minigo.go#L401-L417)・[初期化](../../../minigo.go#L1488-L1512)・[関数リテラル](../../../compile/lit.go#L451-L475)
- [ ] **P4-3b — 引数のその場渡しの対象を確定する**。その場渡しは spread なしの `OpCall` が対象。spread／多値展開、defer、host callback にはスライスの経路が残る。退避・保持が必要な呼び出しは寿命を記録し、その場渡しにできる経路だけ移す。ケース別に結果と alloc を比較する。[呼び出し命令](../../../vm/loop.go#L598-L656)・[通常呼び出しの計測](https://github.com/podhmo/minigo/commit/8aee061dd9c1830387c167feb7eaea79be39719d)
- [ ] **P4-4b — CallSite の記述子と実行時に保存する型情報の境界を明記する**。命令は `CallSite` を参照し、frame の旧バッファは除去済み。spread のモードは命令側にあり、defer は実行時にしか得られない `statics` と `spreadTd` を `callTyps` に保存する。登録元 frame の寿命を考慮し、除去対象は重複した静的メタデータとするのか、保存形式も変えるのかを完了条件に書く。[記述子](../../../bytecode/bytecode.go#L368-L405)・[保存される型](../../../vm/frame.go#L36-L55)・[defer の登録](../../../vm/loop.go#L631-L644)
- [ ] **P4-6 — 通常のコンパイルが発行する `OpEvalFragment` を追う**。改名は済んだ。宣言ファイルの import を使う型 selector、配列長、配列リテラルのキーもこの命令を発行する。宣言スコープを持つ専用の解決経路へ移すか、通常実行で残す用途を計画に明記する。[型 selector](../../../compile/expr.go#L509-L517)・[リテラル](../../../compile/lit.go#L47-L63)・[配列長](../../../compile/lit.go#L431-L447)

## 検証・計測の残件

- [x] **M4-8a/b — 再実行と再コンパイルを分けてテストする**。`TestStrictLazyCompile` は同じ Engine とエントリーパッケージで 2 回実行する。2 回目はロード済みパッケージとコンパイルした関数を再利用するため、事前に index がある状態での再コンパイルを比較できない。依存の index を先に用意した別 Engine で初回コンパイルし、cold と比較する。当初の 15 件は corpus 13 件、HTTP roundtrip、コンパイラの合成型テスト。後二者も strict の受け入れジョブから直接走る検証を足す。`gate_recvdeferred` は後から追加された corpus ケース。[現在の 2 回実行](../../../deferred_facts_test.go#L163-L197)・[パッケージキャッシュ](../../../minigo.go#L1096-L1112)・[受け入れセット](./references/acceptance-set.md)・[CI](../../../.github/workflows/go.yml#L35-L44) → `TestStrictLazyCompile`（cold+再実行）と `TestStrictLazyCompilePreparedIndex`（依存 index 準備済みの別 Engine で初回コンパイル）に分離。`TestStrictLazyHTTPRoundtrip` と `compile.TestStrictLazySelMemberSyntheticTypeEntry` を追加し、strict 受け入れジョブは `./compile` も走る。
- [ ] **P2-2 / P2-3 — `VMCaller` のスキャン結果を実際の使用で分類する**。記載のスキャナは、名前付き引数を持つ関数 218 件、メソッド呼び出しなし 167 件（うち受け渡しのみと判定 156 件）を報告した。メソッドなし・受け渡しなしの 11 件には、たとえば builtin `panic` が caller を `runtime.Panic.VC` に保存する例や、`reflect.DeepEqual` が `HostNilTwiner` にアサートする例が含まれる。識別子の参照を数えるようにし、真に未使用の引数は ABI を保つ `_` または削除、受け渡しだけの helper は必要な capability に絞る。[計測とスキャナ](./references/vmcaller.md)
- [ ] **P2-12c — 初回利用だけのコストを残す**。記録には `NewSession` とリロード全体の測定があるが、移設した installer の初回利用は分離されていない。対象パスごとに新しい Session での初回 install と warm hit を測り、遅延コストを `NewSession` の改善と区別する。[既存の測定](./references/stdlib-lazy.md#p2-12c状態を持つ-installer-の移設)
- [ ] **P2-13 — 未比較の生成パッケージの parity を足す**。export の構造比較は 12 パッケージ。`encoding/base64`、`crypto/sha256`、`net/netip`、`context` は bound/source の同一呼び出しを比べるケース表にない。結果の比較を追加し、`netip` の source 初期化障害は個別に追跡する。成功を期待するケースは両側エラーも失敗にする。[パッケージ一覧と構造テスト](../../../genbind_parity_test.go#L23-L47)・[振る舞いテスト](../../../genbind_parity_test.go#L165-L214)
- [ ] **P4-2c — 約 10% の指標と動的昇格を区別する**。現 HEAD の difffuzz では静的に boxed slot を含む chunk が 438/4,773（9.2%）、boxed slot が 728/12,351（5.9%）。表示される 429 は `ChunksScanned` の値で、実行時昇格の回数ではない。目標を静的分類で判定するならそう明記し、実際の昇格も判断に使うなら Plain → Boxed の遷移を別途計測する。[計測テスト](../../../spike_classify_test.go#L31-L42)・[既存の報告](./references/slot-classification.md#p4-2c-の計測同一-corpus--difffuzz-全件)
- [ ] **全体テスト — `TestScriptProfiler` の parked 判定を安定化する**。再実行で `parked excluded` の失敗を観測した。サンプルに関数名が一度でも現れれば失敗する一方、フィクスチャの関数は park 前と unpark 後にも実行される。park 状態に入っている区間を同期して測るテストを加え、park の前後の正当なサンプルと区別する。[現在の判定](../../../minigo_test.go#L2114-L2147)・[フィクスチャ](../../../testdata/scriptprof/main.go#L15-L28)・[既存の CI 再試行](https://github.com/podhmo/minigo/commit/3ce439ea)
- [ ] **M6-3 / M6-4 — 再検討時の測定条件をそろえる**。両項目とも「実施しない」という判断は記録済み。M6-3 の割合は温かい resolver と 12 ファイルのベンチマークで、realworld の工程内訳は未計測。M6-4 の boxed slot 率と Cell の alloc profile は tagged `Value` が対象とするスカラーの boxing コストを直接示さない。再検討する場合は realworld の locate/parse/index/materialize を分離し、スカラー boxing の CPU/alloc を分けて 15% のゲートを評価する。[判断記録](./references/m6.md)

## 部分完了・見送り項目の扱い

- [ ] **P2-6 / P2-14d — 循環依存を避けて公開 API と実装配置を確定する**。`SymbolID` と `QuotedCall` の移動、registry の抽出は完了。`SpecialFunc` と `SpecialContext` は runtime の型を含み、実装は VM の frame が必要。現配置を採用するなら計画 K8 にも根拠を書き、互換テストを完了条件にする。公開パッケージに型も移す必要がある場合は、まず値への依存を解く最小の API を設計し、既存署名との互換性を確認する。
- [ ] **P2-7 — 抽出済み項目と残す VM 側の責務を確定する**。P2-7a〜f は完了し、VM からの直接 `reflect` import の禁止も通る。VM の typedef／dispatch に残る `hostbridge.Type` 等の利用を許容するなら、親項目にその境界を書いて閉じる。さらに切る場合は実行ロジックを伴う箇所だけ候補にし、単なる別名の追加を完了条件にしない。[アーキテクチャ記録](./references/archcheck.md)
- [ ] **P2-12 / P2-14 — 未移設の installer と root の縮小を同じ一覧で追う**。`stdlibreg.go` の遅延登録には root の `install*` が残る。root 直下の非テスト・非生成 Go は 57 ファイル・17,526 行で、目標は約 3,000 行。`vm/vm.go` は 963 行。残る installer と engine の内部責務を依存順に切り出し、PR ごとに root の行数を報告する。session 単位になった `templateFuncMap` は root TODO の未検証票を見直し、独立した Engine／Session の所有をテストで明文化する。[登録一覧](../../../stdlibreg.go)・[Session のフィールド](../../../minigo.go#L127)・[root TODO](../../../TODO.md)
- [ ] **M1 / K10 — 可変グローバルの監査を追跡対象にする**。計画の K10 には可変グローバルが含まれるが、現状の archcheck は import、共有フィールド、サイズ、禁止呼び出しを検査する。まず生産コードのパッケージ変数を棚卸しし、定数相当、計測用、明示的なプロセス共有、Session に移すものの根拠を記録する。CI に加える場合は許可済みの用途を一覧で管理する。[計画 §8](../../sketch/ja/plan-rearchitecture.md)・[検査の入口](../../../internal/cmd/archcheck/main.go#L59-L78)

## 確認した動作

- `make test` は通過した実行と `TestScriptProfiler` だけが失敗した再実行がある。`make lint`、`make archcheck`、`make test-spike`、`make test-race`、`make concurrency-stress` は対象 HEAD で通過。race のローカル HTTP テストは TCP listen が許可された実行環境で確認した。
- 手元の realworld `grafana-openapi` を同 HEAD のバイナリで実行し、既存 golden と一致した。工程別の時間・alloc はこの結果に含まれない。
- M0-6 のローカルシャドウイングは現在 `LocalShadowingSkipsSpecial` が pin している。M0-8 の逆アセンブル golden、M3 の外部 Session 拒否とオプトイン監査、M5 の Session 分離・版の更新・配列長の所有には実装と対応する検証がある。M6-1/2 の見送りも判断記録がある。
- フォーマット確認で現 HEAD の `internal/archcheck/budgets.go` に整形差分が出る。root TODO にある `runtime/value.go` の import 整形差分は現 HEAD では再現しない。
