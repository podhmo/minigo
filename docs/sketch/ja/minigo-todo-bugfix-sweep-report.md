# minigo TODO バグ掃討レポート — stack #830 (32 PRs)

TODO.md の `minigo: Stack-VM Interpreter` セクションに連なる「明らかなバグ・不足」項目を一括して潰した掃討の記録。発端はユーザー指示「TODO.mdのこれに対応していってほしい。明らかなバグ部分や不足を埋めるタスクが対象」+「バグの存在確認して失敗してたら死ぬテストを追加してほしい」+「すべてのPRのbaseを連鎖させて一本になるようにしてstacked PRsにしてほしい」+「終わったら main..tip の全体差分を子セッションにレビューさせて対応する」の4件。

## スコープ

対象 = TODO `[ ]` のうち設計・性能・境界クラスを除くバグ系全件。対象外として明示したものは **対象外のまま** (unsafe.Pointer 型、GC fidelity、script値の reflect.ValueOf、throughput/HANG、性能候補 L36、inspect body traversal / modulewalk / ImportManager 等の新機構、`[-]` deferred、scriptData eager、realworld grafana rewrite)。

進め方は「各バグを gc / minigo 両側で再現 → 死ぬテスト (difffuzz pin またはユニットテスト) → 修正 → pin 通過 → 1 root cause = 1 PR」。全部の PR を一本の連鎖にした stack **#830** (= 32 PR) として着地。

## round-1: gate 系・pkg store・json 手元実装分 (#827–#838, 11 PR)

### 実施内容（おまけ）

- 閉じられた PR #771 の selNilable 修正を main 起点で再提案 (#827)。qualified 型の `x == nil` を operand gate が通す。
- operand-assignability gate の coverage gap を 4 分割で埋めた (#828 untyped-const 既定型 bind、#829 pointer/string-index/ptr-array operand shape、#831 range/multi-result/select bind、#832 qualified const の runtime tag)。B3 のTODO 1項目が4つの独立 root cause を内包していたため分割。
- B7: switch-case の trap message を gc wording 化 (#833)。`opCmpGate`/`opCaseGate` の判定を `opGate(inSwitch)` に共通化し、文言だけ `trapOpGate` で振り分け。
- B8: proxy box (timerChanBox 等) への reflect メソッド呼出し (#834)。`minireflect.Hooks.UnwrapHost` という inverse hook を新設し、toHost が assignability 失敗時に box を unwrap する。
- `pkg.F.G = v` / `pkg.A[i] = v` の nested store (#835)。`refThrough` の default で `*runtime.Package`/`*runtime.ImportRef` を shared base として返し、`selectMember` が live cell を読む。
- encoding/json 残件3件: Marshaler/Unmarshaler interface typedef (#836)、`**T` target の全ポインタチェーン alloc (`jsonAllocPointee`、#837)、入れ子 UnmarshalJSON エラーの入力順序 (#838 — `callErrPath` + `jsonErrPos` で call error が位置つき競争に参加)。

### 計画外の記録と判断

- **L89 (pkg qualifier store) の TODO 記述は半分陳腐化していた。** `pkg.V += 1`/`pkg.V++`/`pkg.V, x = ...` は #485 の selector lowering で既に動いており、実測で必要だったのは `pkg.F.G`/`pkg.A[i]` の nested だけ。→ 判断: TODO 本文に事実関係を追記した上で、直すのは実実測で壊れていた部分のみ。
- **B7 は wording だけのはずが「判定の共有」も必要だった。** switch clause は `opCmpGate` を通らない別経路だったので、文言を合わせるには先に operand 解決の分岐を `opGate(inSwitch)` として統一する必要があった。→ 判断: 決定ロジック重複を避けるため flag パラメータ化を選択。`exprSpell` は `syntax/lang.go` の `langChecker.render` のパターンを流用。
- **B8 は hook に逃がした。** `toHost` から box を unwrap する最小の方法は minireflect 側に engine 固有知識を入れない `UnwrapHost` コールバック。→ 判断: unwrap された real timer が host メソッドを実行するので、feed の bookkeeping は script 側に残る trade-off を明示コメントで受け入れた。
- **L95 の修正で予定外の改善が出た。** 位置照合のために `ectx.path` を Go フィールド名 (`A`) から入力キー (`a`) への push に変えたら、`UnmarshalTypeError` のテキストが gc 同様 `.a` になり、従来の `.A` 表記ずれも同時に消えた。fold 一致は不要になった (review R14 で dead fold retry を除去)。
- **「want.err が存在する = エラー期待」という pin 契約で一度引っかかった。** 空の want.err を置いて pin が `expected error containing "", but the run succeeded` で落ちた。→ 判断: 成功 pin は want.err を置かない、という運用を確定 (skill 文書にも記載済み)。

## round-2: 残り項目の worker 委譲 (#839–#847, 9 PR)

### 実施内容

- coordinator 側のコンテキストが逼迫したため、残りのバグ項目は worker 子セッション 1本に直列で委譲 (ユーザーの「並列は1まで・取りまとめ役」方針)。
- TextMarshaler/TextUnmarshaler の probe (#839)、anonymous embed の UnmarshalJSON/MarshalJSON 呼出し + promoted pointer-receiver の live slot 書き込み (#840)、fmt `%!v(PANIC=...)` marker (#841)、`panic while printing panic value` の内側型名 (#842)、32bit build の uintptr 定数 (#843)、非 error host 値の型推論 (`hostTypedefOf`、#844)、task-run G6 redeclared index (#845)、G5 `-f` dirscope (#846)、L91 raw-span は設計変更要と判明して finding 記録のみ (#847)。
- L97 (`Member`/`MethodSetOf` が無名 container でも element のメソッドを借用しうる件) は audit の結果 divergence なし → TODO に監査結果を `[x]` 付きで記録。

### 計画外の記録と判断

- **L94 の実態は2バグだった (worker の実測で判明)。** 指示は「bound decoder が embed の field を shape 的に歩く」という decoder 側の話として渡したが、実機では decoder probe が promoted method を既に見えており、失われていたのは**書き込み**側だった — `promotedMember` が埋め込みフィールドのコピーを receiver に bind していたため `*p = x` が生きた slot に届かない。→ 修正は json decoder ではなく vm のメソッド束縛 (FieldRef write-through) に着地。指示の「decoder のバグ」と実際の「vm binding のバグ」で根因が違った典型例。
- **L96 の仕様は実機の方が細かかった。** 指示は「gc の marker を出せ」のみだったが、実機では gc の catchPanic が **nil pointer arg では marker を抑制して `<nil>` を出す**別挙動を持つことが判明 → `fmtNilPointer` まで実装して揃えた (#841)。
- **L90 の実装面は例より広かった。** 指示の例 (map キー + quoted string) は正しかったが、`",string"` 経路の inner text 素通し、`encoding.MarshalerError` 経路 (operand 型名付き)、gc が map キーの MarshalJSON を**無視する**ためキーは Text probe のみ、bare 保存 map キーの宣言型再タグ付け、と4系統の別問題が絡んでいた (#839)。
- **G6 の strict 化は lenient 消費者を壊した (副次問題 → 次レビューの seed)。** index.Build を strict にすると inspect/REPL が redeclared で死ぬため `BuildLenient` を残す二分設計を導入 (#845)。→ この分離自体がレビュー第1弾の B3 (キャッシュ汚染)・B5 (inspect 不整合) を産んだ — 「今日の修正が明日のレビュー指摘になる」直列依存。
- **G5 は Taskfile 自身が constraint で落ちる問題を含んだ。** 「ディレクトリの package を読む」の指示どおり go/build 準拠で実装すると `//go:build task` を持つ Taskfile 自身が除外される → ファイル名ベース選択 + 制約フィルタ適用外という形に着地 (#846)。filename invisibility の穴が残ってレビュー B4 で別 PR 化。
- **L98 の条件は TODO 文より細かかった。** 「内側 panic の型名を出せ」の指示で実施したが、実機では**文字列 payload は raw、型付き payload は gc 型名**という区別が必要で、TODO に書かれていない粒度の gc 仕様だった (#842)。
- **L97 audit は「直すもの」ではなく「調べるもの」だった。** container typedef は Methods を持たず全 surface が gc 一致 → divergence なし、コード変更なしで監査結果を `[x]` で記録。「修正のない `[x]`」も掃討の正常な出力。
- **L91 は「直せるバグ」ではなく「設計問題」だった。** gc は member の生 byte span を method に渡すが、bound decoder は `json.Marshal(dec)` の再 encode を渡すため、空白・数値綴り (`1e2`)・重複キーが原理的に取れない。→ 判断: span 持ち DOM を全再帰に通す設計変更が要ると判明し、正直 defer して TODO に finding を記録 (L91 は `[ ]` のまま)。「直してしまう」ではなく「直せないと判断した」のも掃討の成果の一つ。

## round-3: 全体差分レビュー + fixup (#848–#859, 12 PR)

### 実施内容

- ユーザー指示どおり、別の review 子セッションに `main..devin/1791700439-bindrecv-note` 相当 (実行時の tip = `devin/1791698013-json-rawspan-finding`) の **全体差分 (24 commits, 77 files, +2090/−272)** をレビューさせた。質問は verbatim「バグはないだろうか？/既存の関数の再実装をしてないか？/リファクタリングできないだろうか」。
- 返答は **bug 9件 + reimpl/refactor 9件**。coordinator の要否判断で **全18件「要」** とした (全部 gc 乖離か実害のある設計欠陥/具体的重複で、一つも推測ベースの cosmetic なし)。
- ユーザーの順序指示「バグフィックス全体が先」を守り、同じ worker 子セッションに fixup を続けて投げて **#848–859 の12本**を stack 末尾に追加した。

### バグ側の採用 (全9件)

1. `ectx.callErr` 単一スロットで last-wins → `callErrs` リスト化、位置つき競争に統一 (#848)。round-1 の自分の修正が「次に出るべきエラーが消えてしまう」形を生んでいた — レビューは自分の diff を撃つのが一番効く例。
2. メソッド再宣言を gc 風 `method T.M already declared at <pos>` 1行形式に (#849)。
3. lenient/strict が `e.pkgs`/`e.byDir` を共有してキャッシュ汚染 — DirOf→strict も strict-failed→DirOf も両方向に静かに壊れる設計欠陥 (#850)。
4. `LoadDirScope` に GOOS/GOARCH ファイル名不可視化なし → resolver の `FileScopeFiles` へ列挙を移譲 (B4+R17 統合、#851)。
5. `FileOf`/`PackageOf` が strict のまま → lenient 経由に統一 (#852)。
6. `constKindDte` が `UConst.Rune` を落とす → `UConst.DefaultName()` 再利用に一本化 (B6+R11 統合、#853)。残課題「型付き `==` の不一致チェック」は新規 `[ ]` で TODO に記録。
7. `selNilable` が spec 内部 selector を caller scope で解決 → `unfoldSpecType` で定義側 package scope に (#854)。
8. `hostTypedefOf` が named composite (`type P *T` 等) を KindStruct 化 + 同名 builtin 衝突 → reflect walk に統一 (B8+R16 統合、#855)。difffuzz では観測不能 (bound stdlib に named pointer/array が無い) ので `vm/host_typedef_test.go` でピン。
9. `selMember` の "is type" が `Decl==nil` の synthetic entry を落とす → 旧 `ix.Types[name] != nil` 意味論に復元 (#856)。full-package build では作られないので `compile/sel_member_test.go` でピン。

### リファクタ/再実装側の採用 (全9件、3件はバグに統合)

- `callFuncTyp`/`callOpTyp` の callee 解決を `calleeFuncTyp` に抽出、`nthResultOpTyp` の single-result i=0 を `funcTypeResultOpTyp` に委譲 (#857)。
- `fmtNilPointer`/`jsonNilish` を `peelNil` に統一、jsonShape/jsonStringOpt の recv/cell switch 3重複を `unmarshalRecv` に抽出、`jsonErrPos` の死んでいた fold retry を除去 (#858)。
- `bindRecv` の note を `noteBindVar` (旧 `noteRangeVar`) に統一 (#859)。

### 計画外の記録と判断

以下は worker に「受け取った指示と実際の乖離」を聞いてまとめた実装者側の記録 + coordinator の採用/運用判断。

- **B2 (redeclared wording) の位置情報は実機検証で変わった。** 指示例の `at ./dup.go:5:10` は review 側の観測で、実機では **name-ident の位置** (`main.go:7:10`) が出る — 形式は一致したが位置の出所を実機で確認してから採用 (#849)。
- **B3 (cache 分離) は指示の二択の片方 + 非対称共有を追加した。** 「key by (path, lenient) / separate cache」の二択から separate lenient cache を採用し、加えて指示に無かった「canonical `e.pkgs` のヒットは lenient 参照にも先に答える」一方向共有を残した — 完全分離だと lenient が毎回 rebuild される (#850)。
- **B4+R17 (file visibility) は `//go:build` 適用を意図的に除外した。** `internal/syslist` は import 不可のため KnownOS/KnownArch テーブルを `resolve/filescope.go` にミラー。さらに FileScopeFiles は `//go:build` 制約フィルタを**あえて適用しない** — Taskfile が `//go:build task` を持つからで、go/build の完全再現ではなく filename 不可視化のみ、という指示より限定的な形に (#851)。
- **B6+R11 は想定再現経路が違い、想定下流 divergence が未実装だった。** (a) 裸の `const c = 'a'` は問題箇所を踏まないため、package 修飾 `inner.C` の再現が要った。(b) 指示の想定下流 (`y == i` の rune vs int mismatch) はその比較自体が**未実装**で再現しない → ピンせず新規 `[ ]` として TODO に記録。(c) 修正は `uc.Rune` 直参照でなく `uc.DefaultName()` 再利用を選択 (#853)。
- **B7 の divergence 観測には alias 同輸入名衝突が要った。** selNilable の caller-scope 解決を観測するには `realpkg`/`fakepkg` 同名 import を作る必要があり、加えて gc の unused-import 回避 dummy use が要った — 再現の組み立てコストが指示読みより高かった (#854)。
- **B8+B9 は difffuzz で観測不能 → ユニットテストに変更。** B8 は bound stdlib に named pointer/array 型が無く、B9 は `Decl==nil` synthetic entry が full-package build では生成されない (gc も foreign-receiver をコンパイル不可)。→ ピンを `vm/host_typedef_test.go`/`compile/sel_member_test.go` に置いた。「difffuzz で落ちることを示せないものはユニットテスト」は今回確定した運用。
- **B8 の修正範囲は指示より拡大した。** named composite の誤分類だけでなく、unnamed tail の array Elem / map Elem / func→reflectFuncTypedef、HostNew を全 named 型に広げた — 同じ欠陥の類縁を一括処理 (#855)。
- **R 系の PR 粒度は指示の「1項目1PR」から変えた。** R10+R12 / R13+R14+R15 は同じ関数群を統合する連続項目なので 1 PR まとめに。R18 は `noteRangeVar` 共有化の指示に対し、select 側で使うため `noteBindVar` へ改名も含んだ (#857–#859)。
- **レビュー指摘の全件採用は例外的 (coordinator 判断)。** 通常は cosmetic/推測系が混ざるが、今回は全項目が「gc と比べて動かない形」か「同一機構の重複」で、棄却する理由が一つもなかった。要否判断は「棄却理由があるか」で逆向きに当てた。
- **自分の修正がレビュー指摘の seed になっていた。** B1 (callErr 単一スロット) は round-1 で入れた callErrPath の残滓、B3/B5 は round-2 で入れた strict/lenient 分離の副作用、B8 (hostTypedefOf) も同。subsequent レビューが自分の差分を撃つのが一番有効、という知見を再確認。
- **review 子への出力は途中で truncate された。** `devin_session_interact` の get/structured_output は ~9k chars で打ち切られるため、findings 13–18 は再送させた。→ 判断: レビュー系の structured output は compaction 前提で設計するか、尾を別途再送させる。
- **stack を一本に連鎖させたことの検証価値が実感できた。** sibling-cut 構成の `main..tip` が union にならない事故が前回あったが (difffuzz-todo-sweep-report.md 参照)、今回は branch 連鎖なので `git diff origin/main...<tip>` がそのまま全体差分を出す — レビューが機械的に正しい範囲に効いた。

## 残りの状況

- **L91 (raw-span)** は `[ ]` のまま — span 持ち DOM の設計変更が要ると finding 記録済み。
- 新規 `[ ]` として B6 派生の「型付き `==` の不一致チェック」が TODO 末尾に追加。
- `pkg/locator` の `TestResolvePkgPath/path_outside_module` は main 由来の既存失敗 (この掃討とは無関係)。
- 32 PR 全て CI 緑 (test + e2e)。stack 管理は GitHub が行うため、下位が merge されると自動で base が retarget される。

## 計量

| | PRs | round |
|---|---|---|
| 手元実装 | #827–#838 (11) | round-1 |
| worker 委譲 | #839–#847 (9) | round-2 |
| レビュー fixup | #848–#859 (12) | round-3 |
| 計 | **32** | stack #830 |

difffuzz pin 26 件追加 (json_*、gate_*、fmt_*、task-run_*、pkgstore、reflect_proxybox、selnilable_* 系) + ユニットテスト 3 件 (host_typedef、sel_member、LenientIndexIsolation)。
