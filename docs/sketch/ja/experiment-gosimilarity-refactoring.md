# gosimilarity の出力は minigo のリファクタリングに使えるか

対象は podhmo/minigo `f191f06`（main）と Songmu/gosimilarity `298b2cd`（ネイティブバイナリ）。
先行レポート [experiment-gosimilarity.md](./experiment-gosimilarity.md)（#1022）の「minigo 本体への適用」節（A/B/C/D 分類）を検証し、その先に何ができるかを調べた。
関連レポート: [experiment-gosimilarity-reverify.md](./experiment-gosimilarity-reverify.md)（実行・性能の追試）、[similarity-algorithms.md](./similarity-algorithms.md)（アルゴリズム）。

調査は別エージェントに任せた。主要な主張のうち次の3点は、本文とは別に追試して確認した。

- `strToBytes` の実バグ: Go は `true`、minigo は `false`。
- `scalarKind` と `numericScalarKind` は扱う種別の集合が違う。
- `host_method_typed_const_arg` のフレーク: 15回中1回、30秒でハングした。

凡例: [実行] = 実際に走らせて確認、[読解] = コードやログを読んだだけ。

## TL;DR

- **出力はリファクタリングに役立つ**。`--exclude 'testdata/**' 'examples/**' 'tools/**' --min-tokens 25` の 79 ペアを全件目視で分類した [実行]。
  - 上位20では 80% が統合可能か検討の価値あり。今すぐ安全に統合できるのは 60%。
  - 全件では 46%（今すぐ安全に統合できるのは 27%）。
  - **`--min-tokens 30` が最良で、57 ペア中 61%（同 37%）**。有用なペアを1つも落とさず、誤検出だけを 21 件減らす。
- **先行レポートの数値は再現した**（501 ペア、100% が 43、95% 以上が 165）。ただし解釈は4点訂正が要る。
  - `scalarKind ↔ numericScalarKind` は実コピーではない。前者は Bool/String を含んで Uintptr を含まず、後者はその逆なので、統合すると挙動が変わる。
  - `typeExprOf[T]` の提案は狭すぎる。根本は「`td.Anon` が nil なら `td.Spec.Type`」という3行が18箇所に展開されていることで、`(*TypeDef).TypeExpr()` 1つで置き換えられる。
  - B「意図的ミラーで、消すべき重複ではない」は半分誤り。5組のうち3組は完全に同一か、パラメータが違うだけで、統合してもテストは全部通った [実行]。
  - C の `bigAddVV/bigSubVV` を高階関数化すると約24%遅くなる（1024語で 829→1024 ns/op）。統合しない [実行]。
- **gosimilarity は、層をまたぐ重要なコピーほど見逃す**。
  - パッケージ修飾子の差（`runtime.Named` と `Named`）で減点される。例: `unwrapRef` 90.0%、`lookupDeclIn` 80.1%、`flattenFieldExprs` 83.8%。
  - 抽出したヘルパを呼び忘れたケース（`FieldByName` の末尾と `fieldByNameRec`）は 46.7% で出てこない。
  - 大きな関数の中の断片的な重複（TypeDef 構築の4箇所）は、閾値 0.3 でも見つからない。
- **「ドリフト検出器」としての価値は低い。「新しい重複の誕生検出（ratchet）」に使うべき**。
  - 36 スナップショットで 15 組のミラーを追ったが、類似度が下がったペアは0件だった。
  - 唯一の実際のドリフト（`unsafeBytes`/`strToBytes`）は、類似度がむしろ 79.6→88.9 に上がっていた。
  - 一方、新しいペアは1区間あたり平均 1.4、最大 8 のペースで生まれていた [実行]。
- **統合案 R1〜R6 を scratch worktree に適用し、14 ファイル +256/−708（net −452）で `make format` / `make lint` / `make test` と examples まで全部通った** [実行]。コミットはしていない。

## 再スキャン

main `f191f06`、`--exclude 'testdata/**'`、閾値は既定の 0.87 [実行]。

| 項目 | 今回 | 先行レポート（`7c5d849`） |
|---|---|---|
| 関数数 | 2,362 | 2,348 |
| ペア / グループ | 501 / 32 | 500 / - |
| 100% / ≥95% / ≥95% かつ両側 ≥30 ノード | 43 / 165 / 41 | 43 / 165 / - |
| 両側 <15 ノード | 356（71%） | - |
| 実時間 | 6.5 秒 | - |

閾値と min-tokens ごとのペア数（examples、tools、testdata を除外）[実行]:

| threshold \ min-tokens | 20 | 25 | 30 |
|---|---|---|---|
| 0.87 | 101 | 79 | 57 |
| 0.80 | 165 | 123 | 88 |
| 0.75 | 233 | 172 | 113 |
| 0.70 | 299 | 218 | 150 |

`--threshold 0.95 --min-tokens 25` なら 1.8 秒、0.87 に `--fail-on-duplicates` を付けても 4.9 秒で、CI に常設できる速さ。R1〜R6 を適用した木を同条件で再スキャンすると、ペアは 79→61、グループは 8→5 に減った。

## 先行レポートの A/B/C/D の検証

import の向き（`import` 文を grep で確認）:

- syntax は何にも依存しない。
- index → syntax
- runtime → bytecode, index, syntax
- compile → bytecode, index, runtime, syntax
- vm → bytecode, index, minireflect, runtime, syntax
- root → 上記すべて

vm から root は呼べない（循環する）ので、root と vm で共有したい関数は runtime に置く必要がある。

判定の記号:

- **統合(今)**: 安価で安全。
- **統合(注意)**: 意味論や性能に関わる。
- **同期**: 残したまま、コメントやテストで同期を保つ。
- **誤検出**: 統合すべきでない。

### A. 層をまたぐ実コピー

| ペア | 判定 | 要点 |
|---|---|---|
| `resultTypes` ↔ `sigFieldExprs` ↔ `flattenFieldExprs`（3つ目は 83.8% で見逃し） | 統合(今) | FieldList の展開ロジックが同一。`syntax.FieldTypes`/`FieldCount` に寄せる（R4） |
| `memberDeclOf` ↔ `memberDecl` ↔ `lookupDecl` ↔ `lookupDeclIn`（4つ目は 80.1% で見逃し） | 統合(今) | `(*index.Index).Lookup` に寄せる（R1） |
| `deepTwinOperands` ↔ `twinOperands` | 統合(今) | 100% 同一。`runtime.TwinOperands` に出す（R3） |
| `sigLenKey` ↔ `typLenName` ↔ `lenExprName` | 統合(今) | 100% 同一のコピーが3つ（R3） |
| `arrayTypedef` ↔ `isArrayTyp` ×2 | 統合(今) | 公開済みの `runtime.ArrayTypedef` を呼べば済む（R2） |
| `mapTypeExpr` ↔ `arrayASTOf` | 統合(今)。ただし案を変える | `(*TypeDef).TypeExpr()` を作り、18 箇所の展開、`typedefAst` の 14 呼び出し、`sigAnonOf` を置き換える（R2） |
| `embedBaseName` ↔ `anonFieldName` ↔ `sigEmbedName` | 統合(今) | 公開済みの `runtime.AnonFieldName` がある。compile 側だけ統合済みで、残りが取り残されていた（R3） |
| `unwrapRef` ↔ `Unwrap` | 統合(今) | コードは同一。90.0% は修飾子差による減点（R3） |
| `scalarKind` ↔ `numericScalarKind` | **誤検出** | 扱う種別の集合が違う（`runtime/value.go` の `numericScalarKind` のコメントにも明記されている） |
| `specType` ↔ `specTypeOf`、`boxedSlice` ↔ `boxFields` | 統合(今)・低価値 | 未適用 |

先行レポートに含まれていなかったもの:

| ペア | 判定 | 要点 |
|---|---|---|
| `unsafeBytes` ↔ `strToBytes`（88.9%） | **実バグ** | 「string→script の `[]byte`」を作る2つの関数で表現が違う。`unsafeBytes` は `runtime.ByteValue` の要素と共通の typedef を使い、`strToBytes` は素の `int64` 要素と別の typedef を使う。そのため `reflect.DeepEqual(reflect.ValueOf("hi").Convert([]byte型).Interface(), []byte("hi"))` が Go では `true`、minigo では `false` になる |
| `FieldByName` の末尾 ↔ `fieldByNameRec`（46.7%、見逃し） | 統合(今) | 末尾の 22 行が同一。抽出したヘルパを元の関数で呼び忘れた形（R6） |
| `SetInferred` ↔ `SetInstChunk`（94.8%） | 統合(注意) | ジェネリックな `boundedCache[V]` にできるが、並行性に関わる |
| `pkgTypeSpec`（compile と inspect）、`arrayTypeOf`（minireflect） | 統合(今)・低価値 | 未適用 |

### B. 「意図的なミラー」とされたもの

| ペア | 判定 | 要点 |
|---|---|---|
| `bindsEq`/`bindArgEq` ↔ `…Sig` | 統合(今) | 違いは要素の比較関数だけ。`bindsEqBy(a, b, eq)` にまとめる（R3） |
| `structTagKey` ↔ `sigTagKey` | 統合(今) | 100% 同一 |
| `formatOfNoHash` ↔ `formatOf`（99.7%） | 統合(今) | 違いはフラグの集合だけ |
| `goFieldSpellings` ↔ `typFieldSpellings` | 統合(注意) | 本当に display と identity で違う。件数を数えるループだけ共通化する（R4） |
| `BasicNameOf` ↔ `sizedNameOf` | 統合(注意)・低価値 | 述語だけが違う |

コード中の "mirroring" や "like" というコメントは、分離の意図というより、似ていると認識していることの表明に見える。`sigTagKey` を入れた #523 のテーマは identity の「収束」だった。

### C. 同じファイル内のコピー

| ペア | 判定 | 要点 |
|---|---|---|
| `scriptReader.Read` ↔ `scriptReaderWriterTo.Read` | 統合(今) | 構造体の埋め込みで `Read` を昇格させる（R3） |
| `scriptError.Is`/`As` ↔ `Timeout` | 統合(注意)・**潜在バグの疑い** | `Is`/`As` は `r.(bool)`、`Timeout` は `runtime.Unwrap(r).(bool)` と扱いが違い、名前付き bool を返すメソッドの扱いが食い違う（未実行） |
| `convertMap` ↔ `convertChan` | 統合(注意)・低価値 | 型アサートの対象だけが違う |
| `bigAddVV` ↔ `bigSubVV` | 同期（統合しない） | 高階関数化すると約24%遅い。math/big の asm を置き換えるという存在理由に反する |
| `ProbeResult` ↔ `Judgement`、`scriptIface.Write`/`Read` | 誤検出 | フィールドが偶然重なっただけ、またはアダプタの定型コード |

### D. 「ノイズとして除外」とされたもの

先行レポートの判断（fixture、build tag 版、reflect API のミラー、hook のアダプタ、25〜30 ノードの小関数）はおおむね正しい。ただし reflect API のミラーを分けておく実際の理由は「鏡写しだから」ではなく、panic の文言を difffuzz で Go と一致させてあることにある。`NumIn`/`NumOut` の件数ループは `syntax.FieldCount` に寄せられた（R4）。

## 試したリファクタリング（scratch worktree、未コミット）

| # | 内容 | 差分 |
|---|---|---|
| R1 | `(*index.Index).Lookup` で memberDecl 系の4コピーを統合 | 5 ファイル +29/−68 |
| R2 | `(*TypeDef).TypeExpr()` を追加して、展開済みの 18 箇所などを置き換え | 6 ファイル +64/−155 |
| R3 | `runtime.TwinOperands`、`AnonFieldName`、`TypLenName`、`structTagKey`、`runtime.Unwrap`、`formatDirective`、埋め込み、`bindsEqBy` | 7 ファイル +63/−219 |
| R4 | `syntax.FieldCount`/`FieldTypes` を追加（minireflect が syntax を新しく import する） | 6 ファイル +57/−133 |
| R5 | `TypeDef.SetStructShape`/`SetInterfaceShape` で TypeDef 構築の4箇所を置き換え | 4 ファイル +42/−111 |
| R6 | `FieldByName` の末尾を `fieldByNameRec` の呼び出しにする | 1 ファイル +1/−22 |
| 累計 | 3-way で衝突なく合成できた | 14 ファイル +256/−708 |

累計の状態で `make format`、`make lint`、`make test`、`go test -count=1 ./...`、examples の各モジュールがすべて通った。

- R5 には細かい差が1つある。`localTypeDecl` は空のタグ `""` を FTags に入れていなかったが、`StructFieldTags` は入れる。影響は理論上だけ。
- 適用しなかったもの: `bigAddVV/bigSubVV`（性能）、`scriptError.Is/As/Timeout`、`embedTypeDepth`/`embedTypeMethodDepth`（nil の埋め込みポインタの意味論に関わる）、`boundedCache[V]`。

## S/N 比と見逃し

min-tokens 25 の 79 ペアを、M（統合(今)）、C（統合(注意)）、F（誤検出・意図的ミラー）に分けた。

| 範囲 | n | M | C | F | M+C | M のみ |
|---|---|---|---|---|---|---|
| 上位10 | 10 | 3 | 4 | 3 | 70% | 30% |
| 上位20 | 20 | 12 | 4 | 4 | 80% | 60% |
| 上位30 | 30 | 16 | 8 | 6 | 80% | 53% |
| 上位50 | 50 | 19 | 13 | 18 | 64% | 38% |
| min-tokens 25 全体 | 79 | 21 | 15 | 43 | 46% | 27% |
| min-tokens 30 全体 | 57 | 21 | 14 | 22 | 61% | 37% |

- M の 21 ペアには同じ重複の組み合わせ違い（クリーク）が含まれるので、独立した話としては約 14 件になる。
- 閾値を 0.80 まで下げると、クリークの欠けたメンバー（`lookupDeclIn`、`flattenFieldExprs`、`arrayTypeOf` など）が見つかる。
- 推奨する手順:
  1. 0.87〜0.95 の上位で、重複のファミリーを特定する。
  2. ファミリーの各メンバー名で `--filter-function NAME --threshold 0.7` を実行し、クリークを埋める。

見逃しの型（minigo で目立ったもの）:

| 型 | 例 | gosimilarity の結果 |
|---|---|---|
| 大きな関数の中の断片的な重複 | TypeDef の Fields/EmbedSpecs/FTags などの設定（`compile.go` 2箇所、`minigo.go`、`dispatch.go`） | 閾値 0.3 でも 0 件 |
| 抽出したヘルパを呼んでいない | `FieldByName` ↔ `fieldByNameRec` | 46.7% |
| 層をまたいだときの修飾子による減点 | `unwrapRef` ↔ `Unwrap`、`lookupDecl` ↔ `memberDecl` | 90.0% / 87.9% |
| nil チェックの有無 | `flattenFieldExprs` ↔ `resultTypes` | 83.8% |
| 意味が違うのに似ている | `scalarKind` ↔ `numericScalarKind` | 88.9%（誤検出） |

補完として、正規化した行の8行ウィンドウが完全一致する箇所を探す検出器を試した。数秒で 29 領域が出て、上の断片重複やヘルパの呼び忘れを拾えた。関数単位で見る gosimilarity とは相補的な関係にある。

## その先（優先度順）

- **P0 重複解消 PR**: 小さな PR に分けて出す。順序は R1 → R3 の小物 → `strToBytes` の修正（difffuzz ケース付き）→ R4 → R2 → R5 → R6。効果は net −452 行という数字よりも、層をまたぐコピーが片側だけ修正される事故の起きうる面積が減ることにある。
- **P1 CI に ratchet を入れる**:
  - `--min-tokens 30 --threshold 0.95 --format json` を約 1.8 秒で走らせる。
  - 現在のペア集合を baseline として固定し、baseline にない新しいペアが出たら失敗させる。
  - `--fail-on-duplicates` は既存のペアがあるため常に赤になり、そのままでは使えない。
  - 履歴で再生すると、最大の誕生区間（8 ペア）は #523 を含んでいた。重複を止めるのに一番安いレビュー時点で発火できる。
  - 8行ウィンドウの完全一致も、同じく ratchet にする。
- **P2 ドリフト検出は価値が小さい**:
  - 統合できるものは統合した方が安い。
  - 本当に同期が必要なペアが残るなら、外部ツールより、`go/parser` で両関数の本体を正規化して等しいことを確かめる Go テストの方が堅い。
- **P3 `vm/vm.go` と `intrinsics.go` の分割**:
  - 規模: `vm/vm.go` は 17,435 行で、883 コミット中 305 が触っている。`intrinsics.go` は 10,216 行。
  - パッケージは分けず、同じパッケージ内でファイルだけを分ける。挙動は変わらない。
  - コンフリクトを最小にするため、重複解消の後に行う。
- **P4 その他**:
  - `embedTypeDepth` 系の統合は、nil 埋め込みポインタの difffuzz ケースを足してから。
  - gosimilarity 上流への提案: 修飾子を正規化するオプション、断片（overlap）の検出。

## 気づき（範囲外を含む）

- **既存のフレーク**: `TestDiffRegressions/host_method_typed_const_arg` が、main でもときどき 30 秒ハングする。
  - 調査エージェントの計測では 25 回中 8 回、本文とは別の追試では 15 回中 1 回。
  - スタックダンプでは、script 側の goroutine が `VM.chanRecvRV`（reflect.Select）で待ち、`chanFeed.pump` も select で待っていた。原因は未特定。
- **ツールが拾うべき「取り残し」の形**: 保守側はすでに統合を進めている（#895 の `syntax.TypeParamNames` など）。ただ、その統合が一部にしか適用されていない形の取り残しが多い。例えば compile の `embedFieldName` は `runtime.AnonFieldName` を呼ぶのに、root と runtime のコピーは残っていた。gosimilarity を走らせる価値は、こうした取りこぼしを掃除するところにある。
- **層をまたぐコピーが修飾子で減点される**ことが、A 型（層横断の実コピー）を狙う用途では最大の弱点になる。[similarity-algorithms.md](./similarity-algorithms.md) が挙げた、リテラル値込みで減点される弱点と並ぶ。
