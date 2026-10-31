# 実験: gosimilarity を minigo に適用する + minigo 上で動かす

対象は [podhmo/minigo](https://github.com/podhmo/minigo) @ `7c5d849`、ツールは [Songmu/gosimilarity](https://github.com/Songmu/gosimilarity) v0.0.4 (HEAD)。元ネタの [mizchi/similarity](https://github.com/mizchi/similarity) の docs（TSED 論文・AI アシスタント向けガイド）も併読した。

目的は2つ: (1) AST 類似度ツールを minigo コードベースに適用して、リファクタリングのタネや構造上の傾向を洗い出すこと、(2) gosimilarity 本体を `minigo run` で動かして、実在ツールの実行で minigo のカバレッジと性能を測ること（oapi-codegen 実験と同じ位置付けの負荷サンプル）。

## TL;DR

- gosimilarity は minigo に対して**即戦力**だった。デフォルトスキャン（約20秒）だけで、compile / runtime / vm の3層にまたがる**実コピー10箇所以上**を発見。`memberDeclOf`/`memberDecl`/`lookupDecl` の三重コピーや `deepTwinOperands`/`twinOperands` の完全一致など、人間が grep では拾いにくい「名前だけ違う複製」を網羅的に列挙できる。
- minigo 固有の構造 — 「spelling for identity（`sig*`/`typ*`）と spelling for display」の**並行ミラー実装** — を可視化する「ドリフト検出器」としても有効。100% でない高類似ペアは「意図的にフォークしたが同期すべき場所」と読める。
- 一方、minigo の difffuzz ピン留めテスト群（testdata/difffuzz/*）が**マッチの95.6%を占め**、デフォルト出力は意図的複製に埋もれる。`--exclude 'testdata/**'` が実質必須。
- **dogfooding 実験**: gosimilarity 本体を minigo のインタプリタ上で実行した。2箇所の小さなシム（flagx → flag 代替、`hash/maphash` → FNV 代替）で**完全に動作し、ネイティブと同一の110ペアを再現**した。minigo の lazy な stdlib 解釈（モジュールキャッシュ越境解決を含む）が実用レベルにある実証として使える。
- その過程で minigo 側のカバレッジギャップ3件を発見: `runtime.AddCleanup` 未バインド、`hash/maphash` がハング、`json.NewEncoder` 未バインド（`exec.CommandContext` も未バインド）。

## やったこと

1. gosimilarity を HEAD からビルドし、minigo リポジトリに対して各モードでスキャン（`--format json` で機械処理可能な結果を取得・集計）。
2. 上位マッチの実コードを目視検証し、「実コピー」「意図的ミラー」「ノイズ」に分類。
3. gosimilarity 自体を `minigo run` で実行し、どこまで動くかを検証（mtime に近い用途の実証実験）。
4. mizchi/similarity の docs（TSED アルゴリズム解説、AI 向け運用ガイド）と照合してアルゴリズム上の差異を確認。

## 定量サマリ

| モード | ファイル | 関数 | 型 | 候補ペア | 距離計算 | マッチ |
|---|---|---|---|---|---|---|
| デフォルト（dir walk） | 940 | 4,531 | - | 10,262,715 | 765,898 | 11,224 |
| `--types` 込み | 940 | 4,531 | 1,921 | 11,076,190 | 951,944 | 64,284 |
| `--include-tests` | 1,000 | 4,965 | - | 12,323,130 | 891,204 | 11,283 |
| `--packages ./...` | 58 | 2,011 | - | 2,021,055 | 144,685 | 413 |
| `--exclude 'testdata/**'` | 137 | 2,348 | - | 2,755,378 | 195,307 | 500 |

ネイティブ全リポジトリスキャンは **約20秒**（940ファイル・4531関数）。O(n²) の候補ペア生成をノード数上界で枝刈りする設計で、minigo 規模なら全探索が現実的。

重要な内訳（デフォルトスキャン）:

- **マッチの95.6%（10,727/11,224ペア）が testdata/ 絡み**。difffuzz のピン留め fixture 群は意図的に自己完結コピーされたファイルなので「検出されるのが正しい」が、レポートの実用性を下げる。最大グループは90ファイルにまたがる `try` ヘルパーの複製。
- `--include-tests` してもマッチは +59 ペアのみ。テストファイル同士は意外と重複が少ない。
- `--types` は **+53,060ペア（うち8,060が100%）**。小さい struct 型同士が形だけ似て大量マッチする。`--min-tokens`/`--min-lines` は型には効かない（docs 明記）ため、型スキャンは閾値を上げるなど別途チューニングが要る。
- `--packages` はルートモジュールの58ファイルのみ。examples/ 各モジュール・testdata は対象外。**マルチモジュールリポジトリでは dir walk と package モードでカバレッジがまるで違う**点に注意。
- `--exclude 'testdata/**'` で実コードのマッチは **500ペア**（43 が100%、165 が95%超、321 が90%超、333 がクロスファイル）。これが実質的なレビュー対象量。

## 俯瞰: testdata 除外後の500ペアの内訳

「11,224ペア（95.6%がtestdata）」と「発見リスト」の中間にある分布はこうなる。

### 類似度の分布

| 類似度 | 全体（testdata込み, n=11,224） | testdata 除外（n=500） |
|---|---|---|
| 100% | 7,725 (68.8%) | 43 (8.6%) |
| 95–100% | 2,291 (20.4%) | 122 (24.4%) |
| 90–95% | 465 (4.1%) | 156 (31.2%) |
| 87–90% | 743 (6.6%) | 179 (35.8%) |

testdata はほぼ完全なクローン（68.8%が100%）で、除外後の実コードマッチは 87–95% 帯に山が移る — つまり「似ているが既に分岐している」ペアが主役。レビュー価値が高いのはまさにこの帯。

### サイズの分布（ノード数 = 正規化ASTの大きさ）

| 条件 | ペア数 | 割合 |
|---|---|---|
| 両側 < 15 ノード | 354 | 70.8% |
| 両側 ≥ 30 ノード | 60 | 12.0% |
| 片側 ≥ 50 ノード | 33 | 6.6% |
| ≥95% かつ 両側 ≥ 30 ノード | **41** | **8.2%** |

除外後でも7割は 9–15 ノードの小関数（`loadPath` 系の1行wrapper、`Member`/`Call` 等の委譲）。最大グループ g25（21個）も全部 9–11 ノードの「呼んで返す」形。**実質的なレビュー対象は「両側30ノード以上かつ95%超」の約40ペア**に収斂する — `--min-tokens 30` 相当で直接これに絞れる。

### 場所の集中度

| 関与ファイル/領域 | ペア数 | 割合 |
|---|---|---|
| vm/vm.go が関与 | 177 | 35.4% |
| intrinsics.go が関与 | 60 | 12.0% |
| minireflect/ が関与 | 45 | 9.0% |
| examples/ が関与 | 87 | 17.4% |
| tools/ が関与 | 2 | 0.4% |
| クロスファイル（同一ファイル外） | 333 | 66.6% |

ディレクトリペア別の上位: vm↔vm 39, runtime↔runtime 38, runtime↔vm 27, compile↔vm 15, minigo.go↔vm 14, minireflect↔vm 13, intrinsics↔vm 12, dispatch↔vm 12, complete↔vm 12。**15,000行の vm.go が重複の重心**で、特に runtime↔vm・compile↔vm の層間コピーが目立つ（A節の実コピー一覧の構造と一致）。

## 発見: minigo 側の重複

### A. レイヤー横断の実コピー（統合候補・最有力）

| 類似度 | 箇所 |
|---|---|
| 100% | `compile/compile.go:877 resultTypes` ↔ `runtime/typedef.go:1054 sigFieldExprs` |
| 100% | `complete.go:445 memberDeclOf` ↔ `runtime/package.go:487 memberDecl` ↔ `vm/vm.go:3296 lookupDecl`（87.9% で三重） |
| 100% | `intrinsics.go:9920 deepTwinOperands` ↔ `vm/vm.go:3644 twinOperands` |
| 100% | `runtime/typedef.go:1209 sigLenKey` ↔ `runtime/typedef.go:1650 typLenName` ↔ `vm/vm.go:11448 lenExprName` |
| 100% | `runtime/value.go:1318 arrayTypedef` ↔ `vm/vm.go:11307 isArrayTyp` |
| 100% | `dispatch.go:642 specType` ↔ `vm/vm.go:16302 specTypeOf` |
| 100% | `inspect.go:796 boxedSlice` ↔ `inspect/inspect.go:429 boxFields` |
| 99.3% | `intrinsics.go:7504 mapTypeExpr` ↔ `vm/vm.go:11321 arrayASTOf` |
| 98.5% | `dispatch.go:985 embedBaseName` ↔ `runtime/typedef.go:440 anonFieldName` ↔ `runtime/typedef.go:1171 sigEmbedName` |
| 88.9% | `inspect/inspect.go:1202 scalarKind` ↔ `runtime/value.go:235 numericScalarKind` |
| 90% | `minireflect/rvalue.go:3450 unwrapRef` ↔ `runtime/value.go:285 Unwrap` |

目視確認済み: `memberDeclOf` 系は `ix.Funcs→Types→Consts→Vars` を順に引くだけの同一ロジックが3層に存在。`index` パッケージのメソッドとして一元化できるはず。`mapTypeExpr`/`arrayASTOf` は型アサート先だけ違う同一形 — `func typeExprOf[T ast.Expr](td *TypeDef) *T` のようなジェネリクス化で統合可能。

### B. 意図的ミラー実装ファミリー（ドリフト監視対象）

minigo は型の「spelling」を identity 用（`sig*`）と display 用（`typ*`）に2系統持ち、さらに vm/compile にも並行コピーがある。コメントにも "mirroring typLenName" と明記される** clone-and-own 設計**。

- `bindsEq` ↔ `bindsEqSig` (94.1%), `bindArgEq` ↔ `bindArgEqSig` (93.8%)
- `structTagKey` ↔ `sigTagKey` (100%, 同一ファイル)
- `goFieldSpellings` ↔ `typFieldSpellings` (97.5%)
- `BasicNameOf` ↔ `sizedNameOf` (99.1%) — 判定関数 `basicTypedefName`/`sizedIntName` の差だけ
- `formatOfNoHash` ↔ `formatOf` (99.7%) — フラグ集合 `"+- 0"`/`"+-# 0"` の差だけ

これらは**消すべき重複ではない**が、「片方だけ修正される」事故の温床。gosimilarity の出力は「この同期関係のカタログ」として使える — マージすべきか、同期テストを書くか、の判断材料を網羅的に出してくれる。

### C. 同一ファイル内の完全/準コピー

- `intrinsics.go:5441 scriptReader.Read` ↔ `:5478 scriptReaderWriterTo.Read` (100%, 82 nodes) — プロキシ型2つに同一メソッド。共有埋め込みで消せる。
- `intrinsics.go` `scriptError.Is`/`As` (95.2%), `vm.go:4651 Write`/`Read` (99.3%), `convertMap`/`convertChan` (99.6%) — メソッド名/アサート先だけ違う形。
- `asmimpl.go bigAddVV`/`bigSubVV` (99.5%) — `bits.Add64`/`Sub64` の差だけ。高階関数化可能。
- `tools/difffuzz ProbeResult`/`Judgement` (95%, type) — `Verdict/Want/Got/Detail` を共有する近複製 struct。

### D. 意図的な重複 = ノイズとして除外すべきもの

- `testdata/difffuzz/*` — ピン留め fixture（KindOf/SortedKeys/try 等）が95%超を占める。自己完結が要件なので消せない。
- `examples/*/decoys.go` — 名前の通り「囮」fixture。検出されるのは正常。
- `examples/convert-define/define/methods_go127.go` ↔ `methods_pre127.go` — build tag 別バリアント。dir walk は build constraint を見ないので当然マッチする。
- `minireflect` の `SetLen`/`SetCap`, `NumIn`/`NumOut`, `Pointer`/`UnsafePointer`, `Seq`/`Seq2` 等 — reflect API の鏡面実装。構造は似るが分離が正しい。
- `examples/minigo-generate/tools/enumvals` ↔ `stringer` の `Main` (91.2%, 最高スコア) — 並行ツールの雛形共有。統合するかは好み。
- **小関数ノイズ**: `loadPath`/`loadPathLenient`/`loadDir`/`Member`/`newIterator` のような9-11ノードの「1回呼んで返す」wrapper が93-97%で巨大グループ（21個）を形成。スコアが低いので実害は薄いが、`--min-tokens 25` 前後を推奨（mizchi の docs も同推奨）。

## 実験: gosimilarity を minigo のインタプリタ上で実行

`minigo run ./cmd/gosimilarity -- <dir>` で試行。段階的に潰したブロッカー:

| ブロッカー | 症状 | 対処 |
|---|---|---|
| `runtime.AddCleanup` 未バインド | flagx が FlagSet 登録時に呼び runtime trap | flagx → 約60行の flag shim に置換（`|` 区切りのエイリアス登録のみ再実装） |
| `hash/maphash` | `maphash.MakeSeed()` でハング（120s timeout）— 解釈対象の内部実装が asm 依存 | `hash/fnv` に置換（インターン用途なので等価） |
| `json.NewEncoder` 未バインド | `--format json` で trap | 未回避（テキスト出力のみ検証） |
| `exec.CommandContext` 未バインド | git 発見経路で呼ぶ | `.git` のない対象で回避（walk 経路は純粋 Go） |

**結果**: minigo ソース4〜5ファイルを対象にスキャン完走。`SumAll↔AddPositives` を100%で検出し、実コード対象では **ネイティブと同一の110ペア**を再現。

| 対象 | ネイティブ | minigo 上 |
|---|---|---|
| 5ファイル（vm.go 580KB 含む, 659関数） | 1.0s | 2m39s（約150倍） |

`go/parser`/`go/ast`/`go/token` は lazy 解釈で実用速度（単一ファイル parse はミリ秒）。ボトルネックはマッチ計算と大量ファイルの逐次処理。

**minigo への示唆（TODO 候補）**:

- `runtime.AddCleanup` のバインド追加（Go 1.24+ API、最近のライブラリで使われ始めている）
- `hash/maphash` のハング調査（MakeSeed/Hash の解釈が終わらない — asm フォールバック経路か）
- `encoding/json` の `NewEncoder`/`Encoder` 未バインド補完
- `os/exec` の `CommandContext` バインド（`Command` はある）

## minigo にとっての利用価値

1. **リファクタリング対象の発見機**: 上記A/Cのリストがそのまま TODO/PR のタネになる。特に「名前も置き場も違う実コピー」（`deepTwinOperands`/`twinOperands` 等）は grep では絶対に拾えない。
2. **ミラー実装のドリフト監視**: `sig*`/`typ*` 系の高類似ペアを定期スキャンし、片方だけ変わったかを diff で追う運用。閾値を0.95超に絞れば「ほぼ同期すべきペア」の変化検出になる。
3. **CI/レビュー補助**: `--fail-on-duplicates` は意図的複製の多いこのリポジトリでは厳しい（testdata で必死する）が、`--exclude 'testdata/**' --exclude 'examples/**'` + `--min-tokens 25` 程度なら新規複製の watchdog として使える可能性がある。
4. **dogfooding/ベンチマーク素材**: gosimilarity を minigo 上で動かす試みは minigo の pitch（lazy で安いコード分析）の実証であり、性能ギャップ（約150倍）の計測ベースにもなる。`minigo-usecasefuzz` の realworld ハーネスに gosimilarity をターゲット追加する価値がある。
5. **コード分析ツール同士の相性**: gosimilarity の中身は go/ast 正規化 + 木編集距離で、minigo の inspect/ と同じ問題領域。minigo 側から「この関数に似た既存コード」を引く補助ツールとしてソース組込みもあり得る（依存は flagx のみで軽量）。

## gosimilarity へのフィードバック

良かった点:

- **速い**。O(n²) の上限枝刈り（`similarityUpperBound`）が効き、10.26M 候補 → 765K 距離計算で ~20s。
- 正規化（bound ident の `BoundIdent:i` 化・リテラル種別化）のおかげで変数名違いの実コピーを正確に捕捉。
- `--exclude`/`--include-tests`/`--cross-file-only`/`--packages`/`--format json` と、大規模コードベース向けの絞り込みが揃っている。
- グルーピング（`g1`…）で三重以上のコピーが一目で分かる。

改善できそうな点:

1. **testdata/生成物の支配**: デフォルトだと意図的複製にレポートが埋もれがち（minigo では95.6%）。リポジトリルートの `.gosimilarity` 的な設定ファイル、あるいは `.gitattributes`/`testdata` のデフォルト除外ポリシーがあると嬉しい。
2. **小関数の高類似度ノイズ**: `--min-lines 3` デフォルトでは1行wrapperが95%+で大量マッチする。mizchi docs の推奨どおり `--min-tokens 20-30` を既定に寄せるか、「similarity が高くても nodes が小さいものは別枠」の表示があると良い。
3. **型マッチの氾濫**: `--types` で +5.3万ペア（うち8060が100%）。型には最小サイズフィルタが効かない仕様なので、型専用のフィルタ（最小フィールド数等）が欲しい。
4. **距離の性質の明記**: 実装は APTED/TSED ではなく「ルートから順に子列を整列させるトップダウン編集距離」。文の並べ替え（順序不変の類似）は検出弱いはずで、README の "inspired by similarity" からは TSED を期待しがち。計算式・限界を docs にあると誤解を防げる。
5. **表示パスの美容**: カレント外の絶対パスを渡すと `../../../../tmp/gsample/a.go` 表示になる。絶対パスはそのまま出す方が親切。
6. **`gosimilarity run under minigo`**: 今回の検証で最も引っかかったのは自前コードではなく依存（flagx→`runtime.AddCleanup`）と `hash/maphash`。これは gosimilarity 側の問題ではないが、「依存を増やさない」設計はインタプリタ系との相性が良いという傍証になった。

## 再現方法（minigo 上で gosimilarity を動かす）

gosimilarity のクローンに対して、以下の2箇所だけ手を入れれば `minigo run ./cmd/gosimilarity -- <dir>` が動く（`.git` のない対象ディレクトリを使うこと。git 発見経路は `exec.CommandContext` を呼ぶ）。

1. `gosimilarity.go` の `flagx.XVar(...)` 呼び出しを `xVar(...)` に置き換え、`flagx` import を除去。以下の shim を `flagshim.go` として追加（`"a|b"` 形式のエイリアス登録だけを再実装する）:

```go
package gosimilarity

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

func flagUsage(fs *flag.FlagSet, w io.Writer, usage, desc string) func() {
	return func() {
		fmt.Fprintln(w, usage)
		if desc != "" {
			fmt.Fprintln(w)
			fmt.Fprintln(w, desc)
		}
		fs.PrintDefaults()
	}
}

func eachName(fs *flag.FlagSet, names string, fn func(name string)) {
	for _, n := range strings.Split(names, "|") {
		fn(n)
	}
}

func float64Var(fs *flag.FlagSet, p *float64, names string, v float64, u string) {
	eachName(fs, names, func(n string) { fs.Float64Var(p, n, v, u) })
}
func intVar(fs *flag.FlagSet, p *int, names string, v int, u string) {
	eachName(fs, names, func(n string) { fs.IntVar(p, n, v, u) })
}
func boolVar(fs *flag.FlagSet, p *bool, names string, v bool, u string) {
	eachName(fs, names, func(n string) { fs.BoolVar(p, n, v, u) })
}
func stringVar(fs *flag.FlagSet, p *string, names string, v, u string) {
	eachName(fs, names, func(n string) { fs.StringVar(p, n, v, u) })
}
```

`flagx.Var(fs, &excludes, "exclude", ...)` は `fs.Var(&excludes, "exclude", ...)` に置き換える。

2. `tree.go` の `hash/maphash` を `hash/fnv` に差し替える（インターン用のハッシュなので等価）: `maphash.Seed`/`MakeSeed`/`Hash` を `fnv.New64a()` ベースに書き換え、`seed` フィールドを落とす。

その後は `gosimilarity.Run` の未改造パス（`parser.ParseFile` → `resolveBindings` → 正規化 → 距離比較 → テキスト出力）がそのまま動く。

## 結論

gosimilarity は minigo に対して **「コピー実装のカタログ生成機」として明確な価値**があった。特に compile/runtime/vm の3層に点在する同形 helper の網羅的発見は、人間のレビューでは拾いきれないクラス。`--exclude 'testdata/**'`（+ `--min-tokens` 調整）で信号対雑音比は十分実用域。

minigo から gosimilarity への逆の意味でも面白い結果になった — **gosimilarity は minigo 上で（小さなシム2つで）動く**。実用的な複合モジュールプログラムが minigo の lazy 解釈で完走し結果がネイティブと一致したのは、minigo の実用度を示す良い実例であり、見つかった未バインドAPI（`runtime.AddCleanup`, `json.NewEncoder`, `exec.CommandContext`）と `hash/maphash` ハングは TODO.md に載せる価値のある発見。

## 総評: パフォーマンス考察

上記の実験の後、minigo-usecasefuzz の realworld ハーネス（`gosimilarity-scan`、minigo リポジトリ全体スキャンを指標）で一巡のチューニングを行った。frame pooling + 呼び出しスクラッチ再利用 + range イテレータ回収で wall ~154.5s → ~139.7s（約-10%）、総アロケート 73.2GB → 58.3GB（-20%）を記録。以降の考察はその時点のプロファイルに基づく。

**ボトルネックの形**: 「1箇所を直せば劇的に速くなる」銀の弾丸は残っていない。CPU サンプルの約半分は GC（短命オブジェクトの大量生成に伴う走査・回収）、残り半分はバイトコードのディスパッチ・型強制（coerce）・メンバー解決・マップ操作などインタプリタの固定コストに分散している。つまり遅さは「何か1つの関数が異常」ではなく、命令1個ごとのオーバーヘッドの積み上げ。

**速度の天井**: 同じワークロードのネイティブ実行は ~1.4s で、minigo は ~140s = 約100倍。JIT のないバイトコードインタプリタとしては標準的〜やや良い水準であり、アロケーション削減をさらに詰めても期待できるのはあと10〜30%（~110-125s）。10倍速（~10s 台）には JIT/AOT 化か「スクリプトを Go にコンパイルしてネイティブ実行」というアーキテクチャ変更が必要で、それはもはや別物の実装になる。

**メモリ**: プロファイラ付き実行でピーク RSS 約270MB、終了時点の生存ヒープ約94MB（チューニング前は約111MB）。総アロケート量は巨大だが同時生存オブジェクトは少なく、「短命オブジェクトの生成-破棄が速く回る」パターン。ワークロードの性質上（AST 相当の値とそのラッパーを作っては捨てる）、VM 側で再利用できるのはフレーム・引数バッファ・イテレータ程度で、今回の変更はまさにその層を潰した形。残る削り余地は「1値あたりを小さくする」方向（Value のボックス化削減、マップキーのインターン）で、TODO.md の gosimilarity residuals に記録済み。

**設計思想との整合**: minigo の pitch は「起動速度・ビルド速度の領域で lazy に済ませるコード解析」であり、事前ビルドしたバイナリを回すワークロードの長時間実行は本来の得意領域ではない。リポジトリ全体を舐めて統計を出す gosimilarity は minigo にとって負荷の上限側のベンチマークであり、~100倍という差はこの利用形態では妥当なコストと見なせる。実用上の意味は「minigo でもこの規模の解析が完走し結果が一致する」基準点としての価値にある。
