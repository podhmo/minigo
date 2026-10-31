# mizchi/similarity と Songmu/gosimilarity の調査: アルゴリズム比較と先行レポート主張(a)-(d)の検証

関連: [experiment-gosimilarity.md](./experiment-gosimilarity.md)（元の実験）, [experiment-gosimilarity-reverify.md](./experiment-gosimilarity-reverify.md)（追試）。文中の `/tmp/minigo-workspace/...` は調査時の一時作業領域で、リポジトリには含まれない（再現手順の記録として残す）。

調査対象と版:

- mizchi/similarity `8fa7879` (2026-10-03 時点の HEAD, shallow clone) — `/tmp/minigo-workspace/gosim/research/mizchi-similarity`
- Songmu/gosimilarity `298b2cd` (v0.0.4) — `/tmp/minigo-workspace/gosim/gosimilarity`（ビルド済み検証用バイナリ: `/tmp/minigo-workspace/gosim/research/gosim-298b2cd`、ソースから素直にビルドし直したもの）
- Zenn 記事 https://zenn.dev/mizchi/articles/introduce-ts-similarity（中身は mizchi リポジトリの `docs/introduce-ja.md` と同一）
- 計測コーパス: podhmo/minigo `f191f06`（先行レポートは `7c5d849`。`--exclude 'testdata/**' --exclude 'examples/**'`）
- Go は 1.27.3 (darwin/arm64)。cargo/rustc が無いため **mizchi 側は実行していない（コード読みのみ）**。

凡例: [実行] = 実際に動かして確認、[読解] = ソース/ドキュメントを読んだだけ。

---

## 1. 記事の要点

Zenn 記事（作者 mizchi）の要点。

- 動機: AI にTypeScriptを書かせると `_enhanced` や `_fixed` のような「似たファイル」が増え続ける。それを検出する道具として `similarity-ts` を作った。
- 手法: AST ベースの構造比較。関数（アロー関数含む）同士の意味的類似を検出する。TSED 論文を参考に、APTED（木編集距離）で類似度を出し、さらに実コード量でペナルティをかけてスコア化する。
- 実装史: まず TypeScript で実装 → 全関数ペア比較で V8 ヒープエラー → Rust（`oxc_parser`）へ移植し、TS 版との差分テストで「ほぼ同じ結果」を確認。ただし n(n-1)/2 比較は Rust でも重いので最適化。
- 最適化（記事の数字）: ブルームフィルタ前段（AST ノード種別の出現数から指紋を作り、SIMD 比較で明らかに違うペアを除外。「誤検出を避けるため意図的に甘く」）で約5倍、マルチスレッド（rayon, oxc linter の LintService を参考）で約4倍、他も含め合計約50倍（記事内の「達成できたこと」節では約20倍と記載があり不整合）。約6万行で1秒以内。
- 使い方: `similarity-ts` をプロジェクトルートで実行。出力例は `Similarity: 89.09%, Score: 8.0 points (lines 9~9, avg: 9.0)` と、関数のファイル:行範囲。`--print` でコード表示。`--types` は実装済みだが低速なので記事時点では既定オフ。出力を AI に食わせてリファクタリングさせるのが想定ユースケース。
- 今後: 型リテラル重複の高速化、重複率の計測、複雑な重複の優先度付け、時系列追跡、クラス/関数の相互変換検出、**部分重複（断片）検出**、意味的類似。
- 記事自体に書かれていないもの: 正規化の詳細、閾値、`--min-tokens` の推奨値、サイズペナルティ式、rename cost、overlap の詳細、AI ワークフローの詳細。これらはリポジトリ側の docs/コードにある（次節）。
- 小さな食い違い: 記事が参照する TSED 論文リンク `arXiv 2103.16765` は読者コメントで別論文と指摘されている。リポジトリ内 docs は `arXiv 2404.08817`（Song et al., *Revisiting Code Similarity Evaluation with AST Edit Distance*）を引いており、こちらが TSED の正しい出典。TSED の略語も記事は "Type Structure Edit Distance"、Rust コードのコメントは "Tree Structure Edit Distance"、論文は "Tree Similarity of Edit Distance" と揺れている。

---

## 2. mizchi/similarity の設計と docs 要点

### 2.1 構成
- Rust workspace。`crates/core`（言語非依存: APTED/TSED, 指紋, overlap, 型比較）＋言語別 CLI: `similarity-ts`（Production Ready）, `-py`/`-rs`（Beta）, `-elixir`/`-generic`（Go, Java, C/C++, C#, Ruby）/`-md`/`-css`（Experimental）。README の成熟度表より [読解]。
- **Go サポートは `similarity-generic`（experimental, tree-sitter）のみ**。`crates/similarity-generic/src/main.rs:164-172` は `fs::read_to_string(&path)` で単一ファイルを読み、ファイル内の全ペアを比較する作り。ディレクトリ走査は無い（README は `./src` を渡す例だが、コード上は単一ファイル前提と読める）。`size_penalty: false`, `min_lines: 1` 固定（`main.rs:208-219`）。つまり gosimilarity は「mizchi の Go 版」ではなく「発想を借りて Go 向けに作り直したもの」。[読解]

### 2.2 アルゴリズム（`crates/core/src`）[読解]
- **距離 (`apted.rs`)**: 名前は APTED だが、実体は 322 行の再帰DPで、**本物の APTED ではない**。ノード対 (a,b) について `min(delete_all, insert_all, rename+子列整列)`（`apted.rs:116`）を取り、子列整列は挿入/削除コスト=部分木サイズの系列アラインメントDP（`apted.rs:201-268`）、メモは `(node1.id, node2.id)`。つまり gosimilarity と同種の「トップダウン（Selkow 型）」。旧 TS プロトタイプ (`__deprecated/src/core/apted.ts`) も同様の作り。
- `delete_all`/`insert_all` を**別々に** `min` に入れるため、全く違う部分木同士でも置換コストが `min(size1, size2)` で済む（本来は `size1+size2`）。距離が過小評価になり三角不等式も満たさない。その歪みを後段の大量のヒューリスティックなペナルティで補っている、と読める。
- **正規化 (`tsed.rs:47`)**: `similarity = max(1 - distance / max(size1, size2), 0)`。TSED 論文の式（`TSED = max{1 - δ/MaxNodes(G1,G2), 0}`）と同じ。
- **サイズペナルティ (`tsed.rs:49-112`, 既定オン)**: ①距離0かつサイズ差ありなら減点、②`max_size<10 && dist>0` で ×0.8、`<30` で ×0.9、③`min_size<30` で `×sqrt(min_size/30)`（**距離0でも掛かる**）、さらに `min_size<10` で ×0.5、`<20` で ×0.7、④`size_ratio<0.5` で `×sqrt(ratio)`。加えて `function_extractor.rs:441-464` の `compare_functions` が**もう一段**「平均行数<10 なら ×(avg_lines/10)」を掛ける。結果として完全一致でも 19 ノードの関数は 0.557 にしかならず（`min<20`: `sqrt(19/30)*0.7`）、既定閾値 0.87 を超えるには最小サイズがおよそ23ノード以上必要。つまり**サイズペナルティが暗黙の min-tokens として働く**。`--no-size-penalty` で無効化可。
- **rename cost**: 既定 0.3（`tsed.rs:18`、「TS 実装由来」とコメント）。`compare_values=false`（TS）だが、TS パーサは識別子の**名前を label に入れる**（`parser.rs:237-245`）ので、名前違いは rename コスト0.3/出現で効く。Rust 版は `compare_values=true`（`similarity-rs/src/check.rs:63`）。**スコープ解決も alpha 正規化もしない**（変数名の差は「安いが0ではない」）。論文は rename=1.0, insert=0.8（MBXP/Java での感度分析）を良いとしており、0.3 は論文由来ではない。
- **AST の作り**: TS は `parser.rs` の手書き変換で粗い（未対応の文は `"Statement"` 1ノードに潰す: `parser.rs:121-127`）。tree-sitter 系は匿名トークンを含む CST をそのまま使い、値を持つノード種別だけ `value` に本文を入れる（`generic_tree_sitter_parser.rs:61-80`, Go の value_nodes は identifier/各種 literal/true/false/nil）。
- **前段フィルタ**: `ast_fingerprint.rs`（ノード種別カウント＋ u128 bloom）。`might_be_similar` は「1ビットでも重なれば通す」極めて緩い判定（`:346-365`）、その後 `fingerprint.similarity() >= 0.5`（`fast_similarity.rs:22,87-100`）。ヒューリスティックで厳密な上界ではない。`compute_edit_distance_with_cutoff`（サイズ差下界＋予算打ち切り, `apted.rs:44-199`）という厳密寄りの仕組みもあるが、**使っているのは `similarity-mbt` だけ**（grep 確認）。ts/rs/py のCLIは bloom 前段のみ。
- **ネスト関数**: 親子関係にある関数ペアはスキップ（`function_extractor.rs:46-69, 561-`）。アロー関数・メソッド・コンストラクタも抽出対象。`// similarity-ignore` コメントで個別除外（v0.5.0, `ignore_directive.rs`）。
- **min-lines / min-tokens**: `--min-tokens` を指定すると **min-lines を置き換える**（併用不可。`fast_similarity.rs:51-62`、`similarity-ts/src/main.rs:170-179`）。tokens = 関数本体を変換した木のノード数（`function_extractor.rs:477-`）。`min_lines` が `default_value="3"` の `Option` なので `--min-tokens` 指定時は常に警告が出る作り（推測）。
- **型類似 (`type_comparator.rs`, `type_normalizer.rs`)**: 木編集距離ではなく**プロパティ集合のマッチング**。既定でプロパティ順序を無視（`ignore_property_order: true`）、名前類似度は Levenshtein ベース、`structural_weight=0.6 / naming_weight=0.4`、`property_match_threshold=0.7`、interface と type alias の相互比較可。TS では既定オン（`--types` default true, `--no-types` で無効。記事/`docs/introduce-ja.md` は「既定オフ」と古いまま）。KNOWN_ISSUES.md に Rust enum の類似度が低めに出る既知問題。
- **overlap / 断片検出 (`--experimental-overlap`)**: 関数ごとに部分木の指紋（サイズ・ハッシュ・子ハッシュ）を作り、ノード数 8〜25 のスライディングウィンドウ（`--overlap-min-window 8`, `--overlap-max-window 25`, `--overlap-size-tolerance 0.25`）で、ハッシュ一致なら 1.0、それ以外は子ハッシュの Jaccard で類似度を出す（`subtree_fingerprint.rs:273-380`）。木編集距離は使っていない。
- **config**: `similarity.toml` の読み込みは `similarity-rs` のみ（CHANGELOG 0.5.0）。

### 2.3 CLI オプション（言語別）[読解]
- 共通（README）: `--threshold/-t`, `--min-lines/-m`, `--min-tokens`, `--print/-p`, `--cross-file/-c`, `--no-size-penalty`。
- ts (`similarity-ts/src/main.rs`): 既定 threshold **0.87**（docs は 0.8/0.85 とばらつく）, min-lines 3, rename-cost 0.3, `--types`（既定true）/`--no-types`/`--no-functions`/`--types-only`/`--interfaces-only`/`--type-literals-only`, `--structural-weight 0.6`, `--naming-weight 0.4`, `--classes`/`--classes-only`/`--include-inheritance`, `--no-fast`, `--exclude`, `--experimental-overlap` 系, `--filter-function`/`--filter-function-body`, `--show-ignored`, `--fail-on-duplicates`。
- rs: 既定 threshold 0.85, **min-tokens 既定30**（`similarity-rs/src/config.rs:159-162`）, `--skip-test`, 短い関数にはシグネチャ類似度を混ぜる（`parallel.rs:202-205`: ≤8行で weight 0.35, 他は 0.2）。
- generic: `--language go|java|c|cpp|csharp|ruby`, `--config`, `--threshold`（0.85）, `--show-functions`, overlap 系のみ。

### 2.4 AI リファクタリング向けガイド（`docs/prompt.md`, `.claude/skills/check-similarity*/SKILL.md`）[読解]
- 推奨プロンプト（README）: 「`similarity-ts .` を実行し、重複を分析してリファクタリング計画を立てて。細かいオプションは `-h` で」。
- ワークフロー（`docs/prompt.md`）: ①広域スキャン `similarity-ts src/ --threshold 0.85 --min-tokens 25` → ②ペア絞り込み `--threshold 0.8 --min-tokens 20 --print` → ③見つからなければ 0.75, 0.7 へ段階的に下げる。「有効な閾値」: 0.95+ ほぼ同一（変数名違いのみ）、0.85-0.95 同じアルゴリズムで細部違い、0.75-0.85 構造類似・細部違い、0.7-0.75 関連ロジック。戦略: 0.9 から始め、特定ペアを比較、`--print` で差分確認、共通ロジックを抽出、再実行で確認。
- skill 版: 100% は「必ずリファクタ」、95-100% は「差分をパラメータ化」、85-95% は「共通パターン抽出」、<85% は「関連性を調査」。許容: 5行未満の短いユーティリティ、型パラメータ違いのオーバーロード。CI: `--threshold 0.95 --min-lines 5 --fail-on-duplicates`。
- **`--min-tokens` 推奨**: `docs/prompt.md:18,80`、`RELEASE_NOTES.md:23`、`crates/similarity-ts/README.md:24`、`.claude/skills/check-similarity-ts/SKILL.md:63` に「recommended: 20-30」、例示は 25。ただし**これは similarity-ts（TS）向け**。Rust 向けの `RECOMMENDATIONS.md:19-46` は `--min-tokens 50-100`（一般 50, 厳密 100）。

---

## 3. gosimilarity との差分（表）

| 観点 | mizchi/similarity (ts を中心に) | gosimilarity 298b2cd |
|---|---|---|
| 位置づけ | 多言語。Go は `similarity-generic` の experimental（単一ファイル, tree-sitter） | Go 専用, `go/parser` の完全 AST |
| 木の作り | TS: 手書きの粗い変換（未対応文は `Statement` 1ノード）。tree-sitter: 匿名トークン込みの CST | `go/ast` を reflect で全フィールド走査（`normalize.go:74-143`）。位置/コメント/Obj/Scope は捨て（`:103-114`）、`ParenExpr` は除去（`:61-62`）、`FieldList` は平坦化（`:78-99`）、トークン種別・bool・string フィールドは label に畳み込み |
| 識別子 | label=名前。変数名違いは rename 0.3/出現。スコープ解決なし | スコープ解決（`binding.go`）で変数/型パラメータを `BoundIdent:<kind>:<出現順>` に正規化（`normalize.go:28-31`）。一貫した rename はコスト0。未解決識別子・フィールド名・関数名は `Ident:名前` のまま（差は rename-cost） |
| リテラル | label に値を含む | `BasicLit:<Kind>:<Quote(値)>`（`normalize.go:70`）。値まで有意（種別のみではない） |
| 距離 | 名前は APTED だが実体はトップダウン子列DP。置換コスト `min(delete_all, insert_all, …)`（`apted.rs:116`）で過小評価 | トップダウン子列DP（`tree.go:65-103`）。`min(a.size+b.size, cost+整列)`（`:98`）で正当なコスト。根は必ず対応づけ |
| 正規化式 | `1 - d/max(size)`（`tsed.rs:47`） | 同じ `1 - d/max(size)`（`tree.go:109`）。ただし非同一なら 1 未満に頭打ち（`math.Nextafter(1,0)`） |
| rename cost | 既定 0.3 | 既定 0.3（`gosimilarity.go:72`）、正の値のみ許可 |
| サイズペナルティ | 多段ヒューリスティック（`tsed.rs:49-112` + `function_extractor.rs:441-464`）。既定オン | **なし**（`--no-size-penalty` も無い）。ノイズ制御は `--min-lines`/`--min-tokens` のみ |
| 閾値 | `--threshold` 既定 0.87（docs は 0.8/0.85 と不統一） | 既定 0.87（`gosimilarity.go:71`）。100% 表示は「正規化構文が同一」のみ（`report.go:191-196`） |
| min-lines/min-tokens | 関数のみ。`--min-tokens` が `--min-lines` を置換 | 関数のみ（`analyze.go:79-88`）。`--min-tokens` が `--min-lines` を置換（`:80-81`）。**両方を明示したときだけ警告**（`gosimilarity.go:93-103,181-183`）。単位は「正規化構文ノード数」と docs が明記（`docs/cli.md:16`） |
| 枝刈り | bloom+ノード種別カウント（緩い・非厳密）。サイズ下界+予算の厳密版は mbt のみ使用 | **厳密なサイズ上界** `1-|a-b|/max(a,b)=min/max`（`tree.go:127-129`, `analyze.go:153`）。さらに hash-consing（`tree.go:27-61`）で同一部分木は O(1) |
| 並列 | rayon | 逐次（goroutine なし） |
| 比較対象 | 関数/メソッド/アロー/コンストラクタ、型(interface/alias/literal)、class、overlap。ネスト親子はスキップ | `FuncDecl`（メソッド含む）の**本体のみ**（`analyze.go:62-64`）。シグネチャ・関数名は類似度に寄与しない（`docs/cli.md:126-127`）。`FuncLit`（クロージャ）は親関数に内包され**単独では抽出されない**。`TypeSpec`（関数内のローカル型も）は `--types` で opt-in |
| ジェネリクス | （TS）型パラメータ特別扱いは確認せず | 型パラメータは `BoundIdent:type-param:N` に正規化（`binding.go:71-83`, `receiverTypeParams`）。`func A[T any]` と `func B[E comparable]` の本体は 100%（[実行] `generics`） |
| 型の比較 | プロパティ集合マッチ。順序無視。名前 Levenshtein。構造0.6/名前0.4 | 関数と同じ木編集距離。**フィールド順序に敏感**（[実行] `types_order`: 並べ替えのみで 0.82）。同一 `type_kind` 同士のみ。型名・タグ・フィールド名は有意 |
| 断片/overlap | `--experimental-overlap`（8-25ノードの窓, 部分木ハッシュ+Jaccard） | なし（関数/型の全体比較のみ） |
| グルーピング | 関数はペア列挙。型は `group_similar_types` | union-find で連結成分≥3 を `g1..`（`report.go:138-189`）。"not necessarily all-to-all" と明記 |
| スコア | priority = lines × similarity | `similarity × (linesA+linesB)/2`（`analyze.go:175`）。ほぼ同義 |
| 除外/設定 | `--exclude`, `.gitignore`, `// similarity-ignore`, `similarity.toml`(rs) | `--exclude` glob、生成物/`*_test.go`/vendor/symlink を自動除外、git ls-files、`--packages`（`go list` でビルド制約を反映）。ignore コメント・設定ファイルは**無い** |
| 出力 | VSCode 形式テキスト | テキスト＋`--format json`（宣言/ペア/グループ/診断/summary）、終了コード規約（0/1/2）。agent 向け |
| AI 向けガイド | `docs/prompt.md` と skills 一式 | なし（README に "JSON for scripts and agents" の一文） |

---

## 4. 先行レポート主張 (a)-(d) の検証結果

検証用サンプルは `/tmp/minigo-workspace/gosim/research/samples/*/a.go`（A, B の2関数ずつ）。実行は `scripts/run.sh`（`--threshold 0 --min-lines 1 --format json` で生の類似度を出す）と、gosimilarity の複製に追加したテスト `gosim-exp/exp_test.go`（Zhang–Shasha の本物の順序付き木編集距離と、mizchi 式の距離＋ペナルティの移植を同じ正規化木に当てる）。

### 検証A. 距離の性質（主張 a の根拠）[実行]

| ケース | ノード数 A/B | gosimilarity (トップダウン) | 本物の木編集距離 (Zhang–Shasha, rename 0.3) | mizchi 式の移植 (ペナルティ無 / 有) |
|---|---|---|---|---|
| rename_vars（変数名のみ違う） | 19/19 | 1.000 | 1.000 | 1.000 / 0.557 |
| rename_callee_field（呼び出し先とフィールド名が違う） | 12/12 | 0.925 | 0.925 | 0.925 / 0.369 |
| literal_value（1→2, 10→99） | 10/10 | 0.940 | 0.940 | 0.940 / 0.342 |
| reorder_small（同形の文を入れ替え） | 39/39 | 0.969 | 0.969 | 0.969 / 0.969 |
| reorder_big（同形の if ブロックを入れ替え） | 110/110 | 0.973 | 0.973 | 0.973 / 0.973 |
| **reorder_diffshape**（for と if を入れ替え） | 39/39 | **0.195** | 0.487 | 0.731 / 0.731 |
| **wrapper_if**（全体を `if s.Enabled {…}` で包む） | 83/88 | **0.000** | **0.909** | 0.527 / 0.527 |
| closure_nested（クロージャをインライン化） | 36/24 | 0.228 | 0.617 | 0.642 / 0.574 |
| extract_helper（一部をヘルパーに切り出し） | 83/24 | 0.271 | 0.282 | 0.711 / 0.342 |
| generics（型パラメータ名違い） | 25/25 | 1.000 | 1.000 | 1.000 / 0.913 |
| method_vs_func（レシーバ有無のみ） | 10/10 | 1.000 | 1.000 | 1.000 / 0.404 |

注意: 「mizchi 式の移植」は `apted.rs` の再帰式と `tsed.rs` のペナルティを Go に写して**gosimilarity が作った正規化木**に当てたもの。距離・ペナルティ式の違いだけを切り出す目的で、mizchi の実出力そのものではない（実際の mizchi は識別子を名前のまま持つので `rename_vars` は 1.0 にならない）。Zhang–Shasha 実装は、トップダウンと一致すべきケース（rename/literal/reorder_small/big）で gosimilarity と完全一致したことで整合性を確認している。

### (a) 距離は APTED/TSED ではなく「ルートから順に子列を整列するトップダウン編集距離」で、文の並べ替えは検出が弱い → **前半は正しい。後半は条件付き。さらに mizchi も同じ種類である点が先行レポートに書かれていない**

- **前半 [読解+実行]**: `tree.go:65-103` は、ノード対ごとに子列を挿入/削除=部分木サイズ、置換=再帰距離の系列アラインメントDPで解き（`:84-96`）、`min(a.size+b.size, cost+整列)`（`:98`）で閉じる。**内側のノードだけを削除して子を持ち上げる操作が無い**ため、これは Selkow 型のトップダウン距離。本物の木編集距離（APTED）との差は `wrapper_if` が決定的: gosimilarity 0.000、Zhang–Shasha 0.909（距離 8.0 対 138.9）。クロージャのインライン化 (`closure_nested`) も 0.228 対 0.617。一方、正規化式 `1 - d/max(size)`（`tree.go:109`）は TSED と同じなので、「TSED の正規化 + 非 APTED の距離」が正確な言い方。README/`docs/cli.md` は距離の種類を一切説明していない（`grep` で edit distance/APTED/TSED の言及なし、コメントに "alignment" があるのみ）。
- **見落とされていた点 [読解]**: mizchi の `apted.rs` も本物の APTED ではなく、同じトップダウン子列DP（上記 2.2）。したがって「gosimilarity は mizchi と違って APTED でない」わけではなく、**両者とも「APTED を名乗るトップダウン距離」**。違いは置換コストの定義（mizchi は `min(size1,size2)` で過小評価、gosimilarity は `size1+size2`）、識別子の扱い、ペナルティの有無。
- **後半（並べ替え）[実行]**: 「弱い」は条件付きで正しい。
  - 構造が違う文同士の入れ替え（`reorder_diffshape`）は 0.195 まで落ちる。ただしこれは順序付き木編集距離の性質で、**Zhang–Shasha でも 0.487**（移動操作が無いため、並べ替え=削除+挿入で `2*min(サイズ)`）。トップダウンに固有の弱さではない。
  - 同形の文の入れ替え（`reorder_small` 0.969, `reorder_big` 0.973）は、DP が位置対位置の置換を許すため、リーフの label 差分（rename 0.3 の積み重ね）だけで済み、ほぼ「別物扱いされない」。つまり「検出が弱い」というより「差分が小さく見える」。
  - 最も致命的なのはトップダウン固有の **ラッパー追加/除去・クロージャ化・ヘルパー切り出し**（`wrapper_if` 0.000）。レビュー対象として価値が高い「ほぼ同じだがネストが1段違う」ペアが落ちる。
- 付記: `docs/cli.md:131-132` は「演算子・文の順序・フィールド/メソッド名・未解決識別子・リテラルの綴りは有意」と明記しており、順序有意は仕様。

### (b) mizchi の docs は `--min-tokens` 20-30 を推奨（先行レポートは 25） → **正しいが、TS 向け。Go にそのまま持ち込むのは単位が違う**

- [読解] `docs/prompt.md:18` に「Skip functions with <n AST nodes (recommended: 20-30)」、`:28` の広域スキャン例が 25、`:80` に「20-30 tokens」。`RELEASE_NOTES.md:23`、`crates/similarity-ts/README.md:24`、`.claude/skills/check-similarity-ts/SKILL.md:30,63` も同様。
- ただし (1) これは `similarity-ts` の話で、Rust 向けは 50-100（`RECOMMENDATIONS.md:19,27,46`）、`similarity-rs` の既定は 30（`config.rs:162`）。(2) mizchi の「トークン」は oxc/tree-sitter 由来の木のノード数で、gosimilarity の `nodes`（`go/ast` の正規化木、フィールドリスト平坦化済み）とは尺度が違う。(3) mizchi はサイズペナルティが暗黙の下限（完全一致でも約23ノード未満は 0.87 に届かない）として働くので、`--min-tokens` は主に事前フィルタ。gosimilarity にはペナルティが無いので `--min-tokens`/`--min-lines` が唯一のノイズ制御。
- [実行] minigo（testdata/examples 除外, 2,212 関数）での gosimilarity のノード数分布: 中央値 57, p25=24, p75=120。行数別の中央ノード数は 1-2行:5, 3行:10, 4-5行:17, 6-10行:31.5, 11-20行:69。`--min-tokens 25` は約 6〜7 行に相当。マッチ数は既定（min-lines 3）414 → min-tokens 15: 123 / 25: 79 / 30: 57 / 50: 31。したがって先行レポートの「25 前後」は経験的には妥当だが、「mizchi も同推奨」という根拠づけは単位が異なる点で弱い。gosimilarity 自身は推奨値を docs に書いていない。

### (c) `--min-tokens`/`--min-lines` は型には効かない → **正しい（docs 明記, コード, 実行の三点で確認）**

- [読解] `docs/cli.md:15`「functions only」, `:36`「Filters and minimum sizes never apply to types.」。コードは `analyze.go:79` `if d.Kind == "function" { …minTokens/minLines… }` の中だけでフィルタし、型は素通り（`:86-88`）。`--filter-function*` も関数のみ（`:142`）。
- [実行] `types_tiny`（struct 4つ）に `--types --no-functions --min-tokens 1000 --min-lines 1000 --threshold 0.8` を指定しても `types=4, filtered_functions=0`、`A struct{X int}` と `B struct{Y string}`（6ノード）が 85% で、`C`/`D`（12ノード）が 90% でマッチした。stderr には `--min-tokens overrides --min-lines` の警告。型ノイズ対策は別手段（閾値、`--exclude`、将来の型専用フィルタ）が要る。
- 補足 [実行]: 型はフィールドの**順序**に敏感（同じ4フィールドの並べ替えだけで 0.82）。型名の違いは 0.3/15 ノードで 0.98。

### (d) `similarityUpperBound` による枝刈りが O(n²) を実用にしている → **半分正しい。「O(n²) の列挙」ではなく「O(n²) 回の距離計算」を潰している。かつ厳密（無損失）**

- [読解] `tree.go:127-129` は `1-|a-b|/max(a,b)`、すなわち `min(a,b)/max(a,b)`。`analyze.go:153-157` で `< threshold` のペアを距離計算せずに捨てる（`pruned_pairs`）。編集距離は少なくともサイズ差だけ必要なので、これは真の上界で、結果は変わらない。
- [実行] minigo（testdata/examples 除外, 2,026 関数, 候補 2,051,325 ペア, 既定閾値 0.87）:
  - 枝刈りあり: 距離計算 146,578 回（枝刈り 1,904,736）、マッチ 414、wall 約 5.5〜10 秒（マシン負荷でぶれる）、ピーク RSS 約 58MB。
  - 枝刈りを外した改造版（`gosim-noprune`）: 距離計算 2,051,314 回、**マッチは同じ 414**、wall 約 81 秒（user 138 秒）。**約 14 倍の距離計算削減、wall 約 8〜15 倍の短縮**。
  - 「O(n²) の列挙そのもの」は安い: `--threshold 1`（全ペアを即枝刈り）で parse+正規化+2M ペア列挙まで含めて 0.24 秒。ネイティブ benchmark `BenchmarkRepository/exact-10000` は 5,000万候補ペアの列挙＋パースで 0.50 秒（約 10ns/ペア）。
  - 効きは閾値依存: 距離計算回数は 0.95: 53,819 / 0.87: 146,578 / 0.7: 372,287 / 0.5: 708,505（所要 4s → 28s）。低い閾値では枝刈りの恩恵が薄れる。
- もう一つの要因として hash-consing（`tree.go:15-61`）がある。同一の正規化木は同一ポインタなので `a.tree == b.tree` は距離計算を丸ごと省略（`analyze.go:167-171`）、`visit` 内でも `a == b` で 0（`tree.go:69-71`）。minigo では総ノード 283,326 が 102,181 の相異なる木ノードに畳まれた（36%）。

---

## 5. minigo への示唆

### 5.1 minigo コードベースに gosimilarity を使うとき
- 実用レシピ: `--exclude 'testdata/**' --exclude 'examples/**' --min-tokens 25`（またはより厳しく 30）。ノイズ実測は 414 → 79 ペア（グループ 24 → 8）、`--format json` で agent に渡し、`groups`（連結成分≥3）から統合候補を拾う。mizchi の docs 流に「0.95+ → 0.85-0.95 → 以下」と段階的に読む運用は、gosimilarity の `Score`（類似度×平均行数）順の出力とも噛み合う。
- **盲点（アルゴリズムの限界）**: (1) ラッパー追加/除去・クロージャ化・ヘルパー切り出しで「ほぼ同じ」ペアが落ちる（`wrapper_if` 0.000）。(2) 構造の違う文の並べ替えに弱い。(3) 部分重複（断片）は検出不能。minigo には 15,000 行級の `vm/vm.go` や最大 31,138 ノードの関数があり、巨大関数内の部分複製は全体比較では見えない。mizchi の overlap（8-25ノード窓）に相当する機能は gosimilarity に無い。(4) シグネチャ無視のため、レシーバ違い/引数違いの関数が 100% でマッチする（`method_vs_func`）。(5) クロージャは親に内包されるので、クロージャ単位の複製は見えない。(6) 型は `--types` でフィールド順に敏感、サイズ下限が効かない。
- 先行レポートの記述の訂正候補: 「リテラル種別化」は不正確で、リテラルは**種別と値の両方**が label に入る（`normalize.go:70`、`1→2` だけで 0.94）。「`--min-tokens` 25 を mizchi も推奨」は TS 向け・尺度違い。「実装は APTED/TSED ではない」は mizchi も同様に「APTED を名乗るトップダウン距離」である点を補うと公平。

### 5.2 minigo 上で gosimilarity を動かす（性能の観点）
- **計算コストの正体 [実行]**: ネイティブの CPU プロファイル（minigo 全体スキャン）では `distance.func1`（再帰 `visit`）が累積 47.6%、GC が約 20%、`runtime.mapassign` が 19.6%、map 拡張(rehash)が 12.7%。**割当バイトの 98%（4.25GB / 4.34GB）、割当オブジェクトの 79% が `distance.func1`**（呼び出しごとの `memo := make(map[treePair]float64)` と、非リーフの各ノード対ごとの DP 行スライス2本, `tree.go:66,84-85`）。パース+正規化は合計 32MB 程度。先行レポートの「GC が CPU の約半分、短命オブジェクトの大量生成」と整合する。minigo 上でもここが支配的になる。
- **map のキー**: メモは `treePair{a,b *tree}`（ポインタ2つの構造体キー）。インタプリタでは構造体キーの map 操作が高価になりやすく、`map[treePair]float64` の挿入/検索が主たる内側ループ。
- **実験的な打ち手（ネイティブで確認、いずれも minigo 上では未検証）**: メモを全呼び出しで共有する案は**逆効果**（RSS 2.4GB, wall 約3割悪化）。`clear(map)` で1個を使い回す案は総CPU（user）が約17%減るが、並列GCが効く分 wall は約25%悪化（単スレッドのインタプリタでは user 時間が実際の指標に近い）。大幅な改善は期待薄で、割当削減よりも「そもそも距離計算回数を減らす」方が効く。
- **インタプリタで見えるもう一つの O(n²)**: ペア列挙ループ（`analyze.go:132-161`）は1ペアあたり `Kind` 比較・`strings.Contains` を最大4回（`filter` が空でも呼ぶ, `:142-146`）・`similarityUpperBound`（`math.Max/Abs`+`max`）を実行する。ネイティブでは約10ns/ペアだが、インタプリタが約100倍なら約1µs/ペア → minigo 全体スキャン（200〜1000万ペア）で数秒〜十数秒。上界が `min/max` だけに依存する性質を使い、`Nodes` でソートして窓で走査すれば列挙を距離計算候補数（約15万）まで減らせるはず。上流に提案できる改善案で、ここでは未検証。
- **AST ノードごとの無駄な割当**: `ast.Inspect` のコールバックが毎ノードで `normalizer`（構造体+`make(map[*binding]int)`）を生成している（`analyze.go:96`）。minigo スキャン（53ファイル）で 337,567 コールバック。宣言（FuncDecl/TypeSpec）のときだけ作れば済む。ネイティブでは無視できるが、インタプリタでは per-node のコストになる。
- **reflect 多用の正規化**: `normalize.go:74-143` は全 AST ノード・全フィールドに対し `reflect.Type.Field(i)`, `field.Type.Implements`, `value.Kind()` 等を呼ぶ。minigo の minireflect が負荷を受ける経路。`reflect.TypeFor`（`normalize.go:45-47`）も使う。
- **依存の相性 [読解]**: `flagx`（`github.com/Songmu/flagx@v0.0.2/names.go:16,33-38`）は `weak.Pointer[flag.FlagSet]` と `runtime.AddCleanup` と `runtime.KeepAlive` を使う（先行レポートの `AddCleanup` 未バインドの原因）。`hash/maphash`（`tree.go:24,28-30`）は先行レポートでハング。`encoding/json.Encoder`（`gosimilarity.go:195-197`）、`os/exec.CommandContext`（`discover.go:220` の `git ls-files`, `discover_packages.go:32` の `go list`）、`log/slog` の TextHandler（`report.go:106`）、`go/format`（レシーバ整形, `analyze.go:67`）も使う。`go.mod` は `go 1.27.3`、`for range int`/`max` 組込み/`b.Loop` などの新しい構文を使う。先行レポートの「flagx を shim に置換」「maphash を fnv に置換」の2箇所パッチで動く、という結論自体は妥当（`gosim-patched` の tree.go 差分で確認）。
- **メモリ**: hash-consing の `buckets map[uint64][]*tree`（`tree.go:19`）は相異なるノードだけを保持（minigo で 102K ノード）。各 `node()` 呼び出しで `children` スライス（`make([]*tree, 0, v.NumField())`, `normalize.go:77`）を先に確保し、既存ノードにヒットすると捨てる — インタプリタでは per-node の一時スライスになる。
- **並列なし**: gosimilarity は逐次なので、インタプリタ上でも挙動は決定的。mizchi は rayon 前提でこの意味では移植しづらい。
- **minigo-usecasefuzz への含意**: realworld ハーネスの対象として gosimilarity-scan を使うなら、検証すべき軸は (1) 距離計算ループ（map 構造体キー・DP スライス割当）、(2) reflect 正規化、(3) `ast.Inspect` の per-node 割当、(4) 100倍級のペア列挙。先行レポートの「~100倍」「GC 半分」は上のプロファイルと整合。

---

## 6. 気づき（範囲外含む）

1. **「APTED」の名称問題**: mizchi は旧TS版・Rust版ともに本物の APTED（Pawlik & Augsten）を実装していない。論文（arXiv 2404.08817）の TSED は本物の APTED を使い、rename=1.0/delete=1.0/insert=0.8 が最適とする。mizchi の 0.3 は論文由来ではない。両ツールとも「TSED と同じ正規化式 + トップダウン距離」。
2. **mizchi の距離は計量ではない**: `min(delete_all, insert_all, …)`（`apted.rs:116`）のせいで、無関係な部分木同士の置換が `min(size)` で済む。多段のサイズペナルティはその歪みへの対症療法に見える。[読解 + 移植実験の裏付け: wrapper_if が 0.527 になる等]
3. **mizchi 側の docs の不整合 [読解]**: 既定 threshold が README/docs で 0.8/0.85/0.87 とばらつく（CLI 実装は 0.87）。`docs/introduce-ja.md:165`（Zenn 記事と同文）は `--types` を既定オフと書くが、README と CLI（`main.rs`）は既定オン。README の例にある Go の `similarity-generic --language go ./src` は、コード上は単一ファイル前提。
4. **gosimilarity の良い点**: 識別子のスコープ解決による alpha 正規化（mizchi 側に無い）、厳密な上界による無損失枝刈り、hash-consing、診断/終了コード規約、`--packages` でビルド制約を反映、JSON 出力。`--min-tokens` と `--min-lines` の同時明示だけ警告する設計も mizchi より丁寧。
5. **gosimilarity の弱点（アイデア, いずれも上流への提案候補）**: 距離の種類が docs に無い。ignore コメントや設定ファイルが無い（mizchi は `similarity-ignore` と `similarity.toml`）。型専用の最小サイズフィルタが無い。断片/overlap 検出が無い。シグネチャを完全に無視するため、短い関数では mizchi-rs のようにシグネチャ類似度を足す手もある。トップダウン距離を本物の木編集距離にすると `wrapper_if` 型の取りこぼしは減るが、計算量が増える（Zhang–Shasha は O(n²) 級）ので、枝刈り構造との相性が課題。
6. **範囲外（実装上の細かい指摘）**: `analyze.go:96` の per-node `normalizer` 生成、`analyze.go:142-146` の空フィルタでの `strings.Contains` 呼び出し、`tree.go:66` の呼び出しごとの map 生成は、ネイティブでは無害だがインタプリタ上で効く。`similarity-ts` は `--min-tokens` 指定時に常に警告を出す構造（推測）。
7. **再現物**: `/tmp/minigo-workspace/gosim/research/` 配下に `samples/`（検証サンプル）、`scripts/run.sh`・`scripts/mt.sh`、`gosim-exp/`（Zhang–Shasha と mizchi 式移植の検証テスト, 計測用フック）、`gosim-noprune/`（枝刈りなし改造）、`gosim-memo/`（メモ戦略の実験）。いずれも gosimilarity の複製であり、`/tmp/minigo-workspace/gosim/gosimilarity` の clone 自体は変更していない。
8. **実行していないこと**: mizchi/similarity の実行（cargo 無し）。minigo 上での gosimilarity の再実行（先行レポートの数値を参照したのみ）。mizchi の出力値は移植による推定と読解に基づく。
