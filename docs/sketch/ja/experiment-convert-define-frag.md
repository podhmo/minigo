# convert-define: 式/文の二重経路を `frag` に統一する実験

対象: `examples/convert-define/generator`
発端: [issue #48](https://github.com/podhmo/minigo/issues/48)（text/template を使う generator が読みにくい・エラーメッセージから対処が分からない）
ブランチ: `exp/issue48-frag`（round-2 は `exp/issue49-check`）

---

## 1. 背景と仮説

issue #48 に Claude と Codex がそれぞれ見解を書き、[総論](https://github.com/podhmo/minigo/issues/48#issuecomment-5953285680)では次の 2 つを検証することにした。

1. **変換の実装を 1 つにできるか。** 現状の `generateConversion` 系は `dst != ""` を見て、同じ組み立てを「代入文として書くか」「`func() T { ... }()`（IIFE）で包んで式として書くか」の 2 通りで実装している（`dst != ""` が 12 箇所、`b.WriteString` が 55 箇所）。片方だけ直されてずれていくのが c16/c17 の原因だった。
2. **失敗から原因へ戻れるか。** 整形に失敗すると `main.go` は WARN を出すだけで、整形前のコードを書き出していた。

仮説として、変換関数の戻り値を `frag{stmts []string; expr string}` に統一すれば、次の 2 つの不変条件を守るだけで、ツリーも、プリンターも、スコープ追跡も要らないと考えた。

- **stmts は、対応する expr と同じブロックに、expr の直前に置く。** 制御フロー（nil ガードやループ）は、そのブロックを作る親が持つ。
- **一時変数は、コンバータ内で見えている識別子と衝突しない名前で採番する。**

## 2. 手順

1. **基準を固定する**（コミット `289de81`）。`sampledata` に `SrcShapes`/`DstShapes` を追加した。ptr→value、value→ptr、`**T`、`[]**T`（c16）、`map[K]**T`（c17）、`[]*int`、`[]int→[]*int64`、`[][]int`、`map[int][]*int`（キーの cast とネスト）、`[2]*int`、`*[]int` を網羅する。現行の生成器で `e2e_test/generated.go` を作り直し、`TestGeneratedShapesConversion` で実行時の振る舞いを固定した（値あり／ゼロ値、nil 要素、nil ポインタ、nil スライスが空スライスになること）。この時点の出力には IIFE が 9 個あった。
2. **`frag` へ置き換える。** `getAssignment`/`getMapKeyAssignment`/`generateConversion`/`generate{Slice,Array,Map}Conversion` を削除し、`emitter` のメソッド（`conv`、`ptrToPtr`、`ptrToValue`、`valueToPtr`、`slice`、`array`、`mapOf`）に置き換えた。各形状の実装は 1 つだけになった。
3. **診断を直す。** `format.go` を新設した。

## 3. 設計

```go
type frag struct {
	stmts []string
	expr  string
}

func (f frag) assign(dst string) []string { return slices.Concat(f.stmts, []string{dst + " = " + f.expr}) }
func block(head string, body ...string) string // "head {\n...\n}"、インデントは gofmt に任せる
```

- 親は、子の `stmts` を自分が作るブロック（for の本体、`if x != nil` の本体）に並べ、`expr` をその場で使う。これだけで**ホイストが自然に起き**、IIFE は不要になった。短絡評価の位置のように安全に文を挿入できない場所は、この生成器には存在しなかったので、IIFE で包むアダプターも作らずに済んだ。
- 一時変数を必要とするフィールドは `{ ... }` で囲む（src 側の nil ガードがあれば、その `if` ブロックで代用する）。こうすると一時変数のスコープはフィールド内に閉じるので、**採番はフィールドごとにリセット**できる（`s`、`i`、`item`、`v`、2 個目以降は `s2`…）。
- `fresh(prefix)` は次の名前を避ける。`ctx`/`ec`/`src`/`dst`、テンプレートが固定で import する名前（`context`/`errors`/`fmt`/`model`）、パッケージ修飾のない rule/converter 関数名、そして採番した時点での import alias。

出力例（c16 `[]**T`）:

before:

```go
convertedSlice := make([]**destination.DstLeaf, len(src.SlicePP))
for i, item := range src.SlicePP {
	ec.Enter(fmt.Sprintf("[%d]", i))
	convertedSlice[i] = func() **destination.DstLeaf {
		if item == nil {
			return nil
		}
		tmp := convertSrcLeafToDstLeaf(ctx, ec, (*item))
		return &tmp
	}()
	ec.Leave()
}
dst.SlicePP = convertedSlice
```

after:

```go
s := make([]**destination.DstLeaf, len(src.SlicePP))
for i, item := range src.SlicePP {
	ec.Enter(fmt.Sprintf("[%d]", i))
	var p **destination.DstLeaf
	if item != nil {
		v := convertSrcLeafToDstLeaf(ctx, ec, (*item))
		p = &v
	}
	s[i] = p
	ec.Leave()
}
dst.SlicePP = s
```

## 4. 結果

| 指標 | before | after |
|---|---|---|
| `dst != ""` 分岐 | 12 | 0 |
| `b.WriteString` | 55 | 0 |
| e2e 出力中の IIFE | 9 | 0 |
| `generator.go` の行数 | 1213 | 1252（不変条件のコメント込み） |
| 振る舞いテスト `TestGeneratedShapesConversion` | pass | pass（変更なし） |
| golden の差分 | — | 一時変数名（`convertedSlice` → `s`）と import alias の明示のみ |

行数はほとんど減っていない。減ったのは「同じことを 2 回書く」構造で、増えたのは不変条件の説明である。

### トレードオフ

- トップレベルの ptr→value は少し冗長になった。以前は `if src.X != nil { dst.X = ... }` だったものが、`{ var v T; if src.X != nil { v = ... }; dst.X = v }` になる。経路を 1 本にした代償で、出力の意味は変わらない（`dst` は新しく作った値なので、nil のときはどちらもゼロ値になる）。
- `frag` は文字列のままで、ノードの種類は実質 `block` 1 つだけ。今回の範囲ではこれで足り、自作のツリーやプリンター、jennifer に移る理由は見つからなかった。

## 5. 診断（手順 1）とその副産物

### 整形失敗は非ゼロ終了にした

`formatCode` は `scanner.ErrorList` を `*formatError` として返す。表示する情報は次のとおり。

- 全エラー（10 件まで。それを超えた分は件数だけ）
- エラー行の前後 2 行ずつの、行番号付き抜粋
- **どのコンバータのどのフィールドで起きたか**（直前の `func X(` と `ec.Enter("Field")` から逆引きする）
- **直すべきなのは生成器側**であること。`c.Compute` の式は解析済みの AST から出力し直しているので、生成コードの構文が壊れていたら生成器のバグである。

整形前のソース全体は `-log-level debug` を付けたときだけ出す。こうすれば、出力量に上限を付けつつ、エラーは省略せずに全部出せる。

```
generated code does not parse (2 errors). This is a convert-define generator bug, not a problem in the define file.
Rerun with -log-level debug to dump the full unformatted source.

generated.go:6:14: expected operand, found ')'
  emitted by: converter convertSrcToDst, field Items
    4 | 	dst := &Dst{}
    5 | 	ec.Enter("Items")
  > 6 | 	dst.Items = )
    7 | 	ec.Leave()
    8 | 	return dst
...
```

### 「黙って直さない」検査が、すぐに本物のバグを見つけた

goimports が補った import を検出する `addedImports` を入れた。当初は本番では WARN を出すだけにし、テストでは `strictImports` を有効にして失敗扱いにした（後述のレビューを受けて、本番でも失敗扱いに変更）。すると既存の integration テスト 2 本が即座に失敗した。

- 原因: `TemplateData.Imports` には `im.Imports()` のスナップショットを渡している。ところがテンプレート内の `getQualifiedTypeName` が、そのスナップショットを**取ったあと**で `im.Qualify` を呼び、pair の src/dst 型の import を登録していた。その結果、import ブロックから `source`/`destination` が抜け落ち、毎回 goimports が黙って補っていた。e2e では define ファイル側の import が先に登録されていたため、たまたま表に出ていなかった。
- 修正: 型名を `qualifiedStructName` で emit パスのうちに計算し、`TemplatePair.SrcTypeName`/`DstTypeName` に入れた。`funcMap` と `TemplateData.Im`/`Info` は削除したので、**テンプレートの実行は純粋に整形するだけになった**。

**外部コーパスでの確認（`podhmo/minigo-usecasefuzz` の `convert-define/`、28 ケース）**: main もこのブランチも、28 ケースすべてが期待どおりの判定（OK / GEN-FAIL / BUILD-FAIL）になった。ただし main の生成器に `addedImports` の検査だけを移植して流すと、**8 ケース（c01、c02、c13、c15、c16、broken-dsl、broken-src、stale-generated）で goimports が import を黙って補っていた**。ハーネスでは出力先がケースのモジュール内なので、goimports は正しいパッケージを推測できた。しかし `-dry-run -output /tmp/raw.go` のように出力先をモジュールの外にすると、`example.com/m/destination` のはずが **`.../convert-define/sampledata/destination` という別の同名パッケージ**が補われた。黙って直す処理は、バグを隠すだけでなく、条件次第で間違った依存を入れてしまう。このブランチでは 28 ケースとも補完はゼロだった。

これは、issue #48 で Codex が書いた「`FieldMap.Assign` は emit パスで計算済みなので、テンプレート実行中の副作用という指摘は古い」に対する反例である。フィールド代入については正しいが、型名の修飾という副作用は残っていた。

### ブラインドレビューを受けて: 入力側の経路にも同じ原則を適用した

別のエージェントに、`git log` を見せずに評価してもらった（雑なブラインド）。結論は「生成器の構文失敗は追いやすくなったが、入力ミスと型エラーからの復旧はまだ後段に残る」。指摘はどれも手元で再現した。

| 経路 | before | after |
|---|---|---|
| `-tags 'foo &&'` | 終了コード 0 で `//go:build foo &&` を出力（パーサにとってはただのコメントなので goimports も通る） | 実行前に `go/build/constraint` で検査し、`invalid -tags "foo &&": unexpected end of expression. Fix the command-line arguments: ...` を出して終了。廃止済みの `// +build` 行も出さないようにした |
| define ファイルの構文エラー | `failed to run definition script: loading define file into interpreter: parse ...: 3:12: expected ')', found '{' (and 3 more errors)` の 1 行だけ | `define file ... does not parse (2 errors). Fix the define file; no code was generated.` に続けて、全エラーを行番号付きの抜粋で出す |
| CLI がエラーを表示する方法 | `slog` の属性として出すので、複数行の診断が `\n` にエスケープされて 1 行に潰れる | 短いログ行を出したあと、本文は stderr にそのまま出す |

修正の過程で 2 つのことに気づいた。

- **「全エラーを出す」と、パーサの連鎖エラーがうるさい。** `func main( {` 1 箇所から 6 件のエラーが出た。gofmt と同じく `scanner.ErrorList.RemoveMultiples()` で同じ行のエラーを最初の 1 件にまとめると、別々の 2 行のエラーになった。「省略せずに全部出す」は「重複を除いた発生箇所ごとに全部出す」と読むべきだった。
- **3 種類の失敗を、誰が直すべきかで区別して書く。** 生成器のバグ（`This is a convert-define generator bug`）、define ファイル（`Fix the define file`）、CLI 引数（`Fix the command-line arguments`）。見出しの 1 行目でこれが決まるので、エージェントは次にどのファイルを開くべきかを迷わない。

まだ残っているのは、型の不一致（`int64 → string`）を警告するだけでコマンドが成功する点である。既定の動作は fuzz 実験で「loud failure」として固定した設計なので変えず、`-strict` フラグで警告をエラーにする案と、型エラーの provenance をまとめて別 issue にする。

### TODO の評価を受けて: 自動修復を本番でも失敗扱いにし、provenance の範囲を正した

同じレビュアーに追加した TODO を評価してもらったところ、さらに 2 点の指摘を受けた（どちらも TODO.md に追記されていた）。

- **goimports による import の補完を、本番でも失敗にする。** テストでしか失敗しない状態では、上で見た「間違ったパッケージが補われる」危険が通常の利用では閉じていなかった。テスト専用のスイッチ `strictImports` を削除し、`formatCode` は常に `*missingImportsError` を返すようにした。報告には次を含める。
  - 補われた import の path と、コード中で参照されている名前
  - その名前が最初に使われた箇所の converter/field と抜粋
  - 出力は書いていないこと

  変更後もコーパスは 28/28 のままだった。
  ```
  generated code uses 1 package(s) the generator did not import. This is a convert-define generator bug (ImportManager missed a registration), not a problem in the define file; no output was written.

  missing import "strings" (referenced as strings.)
    first used by: converter convertSrcToDst, field Name
      11 | 	ec.Enter("Name")
    > 12 | 	dst.Name = strings.ToUpper(src.Name)
      13 | 	ec.Leave()
  ```
- **provenance がフィールドの外のエラーまで、そのフィールドに帰属させていた。** 直前の `ec.Enter("X")` を探すだけで、`ec.Leave()` を見ていなかったためである。実は私が書いた format テストの期待値自体が、`return dst` 行のエラーを `field Items` としていた。テストを書いた本人がそれをバグだと気づかず、期待値として固定していたことになる。逆方向に走査するときに Enter/Leave の対応を数えるように直した（要素ごとの `ec.Enter(fmt.Sprintf(...))` はフィールドの内側に入れ子になる）。フィールドの外のエラーは `converter X (outside any field)` と表示する。
- **型エラーの追跡のために `go build` を必須にはしない**、という判断にも同意する。この生成器には「入力パッケージが一時的にコンパイルできなくても再生成できる」という価値がある（usecasefuzz の `stale-generated`/`broken-*` ケース）。ビルド検査を設けるなら任意のモードにし、まず「生成位置 → 変換の判断」の対応表を残すところから始める（TODO.md）。

### 残課題の 2 件を片付けた: 同じパッケージの識別子と `-strict`

- **同じパッケージの識別子との衝突は、main に実在するバグだった。** 同じパッケージに `type item struct{...}` があり、`[]*item → []item` を変換すると、main は `for i, item := range ...` の中で `var z item` を出力し、`item (local variable) is not a type` でコンパイルできなかった（使い捨てのモジュールで main とこのブランチの両方に流して確認）。`generator.PackageIdents` が出力先ディレクトリの `.go` を `go/parser` だけで読んでトップレベルの識別子を集め、`fresh` がそれを避けるようにした（`item2`）。型検査も `go list` もしないので、入力パッケージが壊れていても動く。パースできないファイルからも、パーサが回復できた分の宣言は拾う。予約しすぎても害はないので、build tag や古い生成物も区別せずに読む。
- **`-strict` を追加した。** 生成時の警告（rule でも cast でも変換できない leaf の組）を `*WarningsError` にし、`converter: dst.Field: reason` の一覧と入力側の直し方（`define.Rule` を足す、または `c.Convert` を使う）を出す。出力は書かない。既定の動作（警告を出して出力する）は、fuzz 実験で固定した設計なので変えていない。
- **任意の `-check`（`go build` の型エラーを converter/field に逆引きする）は [issue #49](https://github.com/podhmo/minigo/issues/49) に切り出した。** `-strict` で拾えないものがどれだけ残るかの実例を集めてから判断する。

### README に「Debugging」の節を追加した

使い手向けに、フラグの一覧、失敗の 1 行目から誰が何を直すべきかを引ける表、典型的なデバッグの手順（`-dry-run` → `-log-level debug` で `parsed_info` を見る → CI では `-strict`）を書いた。

## 6. 2 つの見解の検証

| 主張 | 結果 |
|---|---|
| Claude「関数ごとの連番があればスコープ管理は不要」 | **言いすぎ。** 見えている識別子を予約する必要があった。Codex が衝突の例に挙げた `Pair.Variables` は、どの DSL 呼出しからも値が入らない死んだフィールドだったので削除した。同じパッケージの識別子との衝突は、main に実在するバグだったので、出力先パッケージのトップレベルの識別子も予約するようにした（§5）。 |
| Codex「stmts を機械的に追加すると、実行条件や回数が変わりうる」 | **原理としては正しいが、この生成器では起きなかった。** 親が必ず自分のブロックに子を置くので、不変条件を明文化するだけで済み、「どのブロックへ追加するか」という表現は要らなかった。 |
| Codex「テンプレート実行中の副作用は解消済み」 | **一部が誤り。** 上で見たとおり、型名の修飾が残っていた。 |
| 両者「jennifer は要らない」 | 今回の範囲では、そのとおりだった。 |

## 7. 計画外の遭遇と意思決定

計画（総論の手順 1〜3）になかったが、途中で遭遇して判断したもの。

### D1. 基準の固定を先に入れた

計画では「1 経路だけ `frag` に移して golden の差分を見る」だった。しかし既存の golden には IIFE が 1 つもなく、ネストした形状の出力は c16/c17 の fuzz ケース（リポジトリ外）でしか確認できていなかった。そこで、リファクタの前に `SrcShapes` と振る舞いテストを入れて現行の出力を固定した（`289de81`）。差分は golden の比較ではなく、実行時の振る舞いで判定した。

### D2. 1 経路ではなく全形状を一度に移した

1 形状だけ移すと、残りの形状との境界に「frag → IIFE」の変換を一時的に挟む必要がある。ところがその変換こそが、なくしたい二重経路そのものだった。形状は 7 種類で、振る舞いテストで守られていたので、一度に移した。

### D3. 一時変数の採番をフィールド単位にした

最初は関数単位で採番し、`v10` のような名前が出た。フィールドごとに `{ }` でスコープが閉じるので、フィールド単位でリセットしても不変条件は崩れない。出力の読みやすさを優先した。

### D4. 「goimports が補った」検査を足したら、既存のテストが落ちた

計画にはなかった検査で、テンプレート実行中に import を登録している副作用が見つかった（§5）。直し方として、`funcMap` を残して呼ぶ順番を工夫する案もあった。しかし、テンプレートから状態を持つオブジェクト（`Im`/`Info`）そのものを取り上げる方を選んだ。テンプレートを「整形するだけ」にしておけば、同じ種類のバグが構造上起きなくなるからである。

### D5. golden の import alias が変わることを受け入れた

D4 の修正で、integration テストの golden の import が `"example.com/m2/destination"` から `destination "example.com/m2/destination"` に変わった。e2e の出力は以前からこの形だったので、生成器が自分で書く形にそろったとみなして受け入れた。

### D6. `Pair.Variables` は予約ではなく削除

Codex が衝突の例に挙げた `Pair.Variables` は、どこからも値が入らない死んだフィールドだった。予約処理を残す案もあったが、ユーザーと相談して削除した。

### D7. 連鎖エラーは同じ行をまとめる

「エラーは省略せずに全部出す」方針で、`func main( {` 1 箇所から 6 件のエラーが並んだ。件数で打ち切るか、最初の 1 件だけ出すかも考えたが、gofmt と同じ `RemoveMultiples`（同じ行のエラーは最初の 1 件だけ残す）を選んだ。別々の行で起きた独立のエラーを落とさずに済むからである。

### D8. CLI のエラー出力を slog から外した

プロジェクトでは、ログに `log/slog` を使う規約になっている。しかし複数行の診断を slog の属性として出すと、`\n` がエスケープされて 1 行に潰れる。そこで、ログ行（`convert-define failed`）は slog で出し、診断の本文はユーザー向けの出力として stderr にそのまま出すことにした。診断の本文は「ログ」ではなく出力だ、という判断である。

### D9. 本番でも import の補完を失敗扱いにした

最初はテストでだけ失敗にした。既存のユーザーの実行を壊すのを避けるためである。しかし、レビューで「通常の利用でも閉じるべき」と指摘を受けた。usecasefuzz コーパスが 28/28 のまま変わらないことを確かめたうえで、本番でも失敗にした。ハーネスの外では、間違ったパッケージが補われることを実際に確認していた（§5）ことも判断の根拠になった。

### D10. DSL の誤用エラーは、今回は直さなかった

README の Debugging 節を書いている途中で、DSL の誤用エラー（`c.Map(dst.W, src.W)` など）だけは 1 行目で「誰が直すか」を言っていないことに気づいた。README は実態に合わせて書き（位置とトレースバックがあることを明記）、直すのは TODO に回した。エラーの発生源が minigo の runtime trap で、範囲が convert-define の外に広がるためである。→ 派生ブランチ `exp/issue48-dsl-errors` で、minigo 本体の変更を含めて対応した（§8）。

### D11. 満足度評価の 2 件は、どちらも convert-define 側で直した

別のエージェントによる満足度評価で、2 件の指摘を受けた。

- 存在しない `-file` を渡すと、`failed to run definition script: loading define file into interpreter: parse ...: no such file or directory` という、層を重ねただけのエラーになる。直し方が書かれていない。
- `c.Compute(dst.Value, src.N)`（int を string に入れる）が `-strict` でも通り、`go build` で初めて失敗する。

最初に、minigo 本体の変更が必要かどうかを判断した。前者は CLI の入力検査なので、convert-define だけの問題である。後者も、必要な情報はすでに minigo の公開 API で取れる。src のフィールドの型は `model.ResolveFieldPath` で、関数の結果型は `ResolveSymbol` → `Index.Funcs` → `inspect.SignatureOf` で取れる。そこで、どちらも base ブランチ（exp/issue48-frag）で直し、派生ブランチ（exp/issue48-dsl-errors）はその上に rebase することにした。

前者は、`run` の最初で `os.Stat` するようにした。`-tags` と同じく「Fix the command-line arguments」で始まり、ディレクトリを渡した場合も同じように扱う。

後者は、型を推論する範囲を意図的に狭くした。minigo には型検査器がない（`go/types` は使わない制約がある）。そこで、型検査器なしで型が確定する形だけを推論する。一つは src のフィールドパス（`src.A.B`）、もう一つはジェネリックでない、結果が 1 つのパッケージ関数の呼び出しである。それ以外の式（`src.S + "!"`、ジェネリック関数の呼び出し）は「不明」として扱い、警告は出さない。誤検知を出すくらいなら黙る、という方針である。名前付き型と型リテラル（`[]T` など）の組も、基底型を通して代入できる可能性があり、`go/types` なしでは判定できないので黙る。

### D12. 型推論のテストを書いたら、import の登録漏れが見つかった

`funcs.Itoa(src.N)` のテストケースは、型の判定より前に、D9 で入れた検査（goimports が import を補ったら失敗）で落ちた。`c.Compute` の式の中でだけ参照されるパッケージが、`info.Imports` に一度も登録されていなかったのである。`c.Convert` のコンバータのパッケージは登録されていたが、`c.Compute` の式は文字列のまま出力されるだけだった。

既存の integration テストが通っていたのは、同じ define ファイルの `c.Convert(dst.Contact, src.ContactInfo, funcs.ConvertSrcContactToDstContact)` が、同じ `funcs` パッケージをたまたま登録していたからにすぎない。main では goimports が黙って補うので、このバグは見えなかった。いまは、式の中の `pkg.Name` をすべて辿って登録している。D9 の検査が、入れてすぐに 2 件目のバグを見つけたことになる。

## 8. 派生: DSL の誤用エラー（minigo 本体の変更を含む）

ブランチ `exp/issue48-dsl-errors`（`exp/issue48-frag` から派生）で D10 に対応した。

### 何が起きていたか

```
failed to run definition script: evaluating define file: runtime trap: .../define.go:12:2: error while parsing mapping function: source: field path "W": A has no field "W"
Traceback (most recent call first):
...
```

これには 2 つの問題があった。

- **誰が直すかを言っていない。** しかも位置が `12:2`（`define.Convert` の呼出し）で、本当に壊れている `c.Map(dst.W, src.W)` の行（13 行目）ではなかった。
- **構造化した情報が VM で失われる。** special form のハンドラが返したエラーを、VM は `panic(&runtime.Trap{Pos: ins.Pos, Reason: err.Error()})` で文字列に潰していた。ホスト（convert-define）は、自分が返した独自型のエラーを `errors.As` で取り戻せなかった。

### 変更

- **minigo 本体**: `runtime.Trap` に `Err error` と `Unwrap()` を追加した。`Reason: err.Error()` で Trap を作っている 8 箇所（special form、builtin、ホスト呼出し、defer、range-over-func など）すべてで `Err: err` も設定する。VM 自身が出す Trap は `Err == nil` のまま。`TestSpecialFormErrorUnwrap` で、独自型のエラーが Trap 越しに `errors.As` でき、Trap の frames も読めることを固定した。
- **convert-define**: ハンドラは `internal.DefineError{Pos, Msg}` を返す。位置は、失敗した `c.Map`/`c.Convert`/`c.Compute` の呼出しを指す（`mappingWalker` が失敗した呼出しを記録する）。`Run` は Trap から `DefineError` を `errors.As` で取り出し、Trap の frames を付けて返す。CLI は次のように表示する。

```
define file DIR/define.go is invalid at 15:3: c.Map: destination: field path "W": B has no field "W"
Fix the define file at that position; no code was generated.

    13 | func register() {
    14 | 	define.Convert(func(c *define.Config, dst *destination.B, src *source.A) {
  > 15 | 		c.Map(dst.W, src.V)
    16 | 	})
    17 | }

reached via (most recent call first):
  File "DIR/define.go", line 14, in register()
      define.Convert(func(c *define.Config, dst *destination.B, src *source.A) {
  File "DIR/define.go", line 10, in main()
      register()
```

これで README の「失敗の 1 行目が、誰が直すべきかを示す」は、例外なく成り立つようになった。usecasefuzz のコーパス（neg01〜03 の GEN-FAIL を含む）は 28/28 のまま。

### D13. 最初は本体に手を入れずに済ませようとした

最初は「範囲が convert-define の外に広がる」ことを避けて、convert-define の中だけで完結させた。ハンドラを包む関数が `DefineError` を Runner に保存しておき（`r.dslErr`）、`Run` で Trap を受け取ったらそれを返す、という脇道を通す方式である。動きはしたが、ユーザーから「本体に手を入れて。そのためにブランチを分けた」と指摘を受けた。本体側で `Trap.Unwrap` を用意すれば、脇道はまるごと消える。結果として、ホスト側の状態が 1 つ減り、すべての special form のホストが同じ仕組みを使えるようになった。

**教訓**: 「範囲を広げない」ことを優先しすぎて、根本の原因（Trap がエラーを文字列に潰す）を迂回する設計を選んでいた。ブランチを分けたという事実自体が、範囲を広げてよいという合図だった。

### D14. 呼出し経路は、ヘルパーを経由したときだけ出す

DSL ファイルはふつう `main` の中で `define.Convert` を呼ぶので、frame は `main` の 1 つだけになり、表示しても情報が増えない。frame が 2 つ以上あるとき（ヘルパー関数を経由したとき）だけ `reached via` を出すことにした。出力量を抑えつつ、必要なときには経路が見える。

## 9. round-2: `-check` で型エラーを converter/field に戻す（issue #49）

§9 までの残課題として [issue #49](https://github.com/podhmo/minigo/issues/49) に切り出していたもの（ブランチ `exp/issue49-check`、PR #52）。生成コードの構文エラー、import の登録漏れ、既知の leaf の不一致は、生成時に「誰が直すか」を示せるようになった。残っていたのは、**構文は正しいのに `go build` で型エラーになる**ものである。

### 9.1 先に実例を集めた

issue には「着手する前に、`-strict` で拾えずに `-check` でしか拾えない型エラーの実例を集めること」と書いてあった。作り込みすぎを避けるためである。

まず usecasefuzz の既存コーパス（28 件）を見た。`generated.go` で型エラーになるのは `leaf-mismatch` の 1 件だけで、これは `-strict` で拾える。`broken-src` と `broken-field` は入力パッケージ自体の型エラーである。つまり、**既存のコーパスには `-check` でしか拾えない実例が 1 件もなかった**。コーパスが足りていない。

そこで、`-strict` をすり抜けそうな形を 1 つの探索用パッケージにまとめて、`go build` の結果と突き合わせた。

| 形 | `-strict` | `go build` | 分類 |
|---|---|---|---|
| `c.Compute(dst.A, src.A+"!")`（string を int に） | 誤った警告（§9.2 の 2） | 型エラー | 任意の式。`-check` の担当 |
| `c.Compute(dst.D, funcs.Same(src.D))`（ジェネリック） | 誤った警告（§9.2 の 2） | 型エラー | ジェネリック。`-check` の担当 |
| `int` → `fmt.Stringer` | 黙る | 型エラー | メソッド集合が必要。`-check` の担当 |
| `IDs []int` → `Names []string` | 黙る | 型エラー | 型検査器なしで分かるはず（TODO） |
| `c.Convert(dst.E, src.E, func(n int) int {...})` | 黙る | 引数の数のエラー | 構文で分かる → DSL エラーにした |
| dst の型に `fmt.Stringer`、map のフィールドあり | — | `fmt redeclared` | **生成器のバグ**（§9.2 の 1） |

### 9.2 実例集めで見つかったバグ

1. **`fmt` を 2 回 import していた。** テンプレートは `context`/`errors`/`fmt`/`model` を固定で書き出す。一方で ImportManager はそれを知らないので、dst の型が `fmt.Stringer` だと `fmt` をもう 1 回登録していた。ユーザーの別の `errors` パッケージがあれば、alias も衝突しうる。テンプレートの固定行を消し、4 つは ImportManager に先に登録するようにした。import の出所は ImportManager の 1 つだけになる。代わりに出力は `fmt "fmt"` のような冗長な alias 付きになる（パッケージ名はディレクトリ名と一致するとは限らないので、alias は一律には省けない。TODO に記載）。
2. **`c.Compute` の先のフィールドにも、自動マッチの代入が出ていた。** `c.Compute(dst.A, ...)` があっても、同じ名前の `src.A` から `dst.A = src.A` が出力され、そのあとで上書きされていた。型が違えばこの死んだ代入がコンパイルできず、`-strict` も（正しく直したはずの）フィールドを誤って報告する。別の src から書く `c.Map(dst.W, src.X)` も同じだった。トップレベルのフィールドを丸ごと書く場合は、自動マッチの対象から外した。`dst.Inner.ID` のようなネストしたパスは、従来どおり「祖先を自動で入れてから上書き」する。
3. **`c.Convert` の関数リテラルのシグネチャを見ていなかった。** 生成コードは `f(ctx, ec, src)` と呼ぶのに、1 引数のリテラルも受け付けていた。パッケージ関数の場合はすでに検査していたが、リテラルの場合は抜けていた。引数と結果の数は構文から分かるので、DSL エラー（`Fix the define file at that position`）にした。**既存のパーサーテスト 2 件が、この 1 引数リテラルを正しい入力として固定していた。** パーサーだけを見るテストだったので、生成されたコードがビルドできないことに誰も気づかなかった。

1 と 2 は main でも再現する。usecasefuzz の `c20-fmt-iface` と `c21-claimed-dst` は、修正前の main では BUILD-FAIL になることを確認した。

### 9.3 `-check` の設計

- **`go build -overlay` で検査する。** 生成結果を一時ファイルに置き、overlay で出力先のパスに差し込んで、出力先パッケージを `go build` する。出力先には何も書かずに済むので、「失敗したら何も書き出さない」という原則を保てる。`-o /dev/null` で main パッケージのバイナリも捨てる。`-gcflags=-e` で、10 件で打ち切らずに全部のエラーを出す。
- **生成ファイルのエラーは provenance に戻す。** コンパイラは overlay 側の一時ファイルのパスで報告するので、出力先のパスとどちらでも照合する。照合したエラーは、構文エラーのときと同じ `syntaxError` の描画（`emitted by: converter X, field F` と抜粋）に流した。新しい描画は書いていない。
- **それ以外のエラーは「判定不能」とする。** 入力パッケージや依存先が壊れていると、生成ファイルを判定できない。このときは「入力を先に直すか、`-check` なしで再生成する」と返す。issue で「無視するか、判定不能として報告するか」を決める必要があった点への答えである。無視すると、壊れたまま「検査に通った」ことになってしまう。
- **既定にはしない。** 再生成は、パッケージがコンパイルできない最中にこそ必要になる（`stale-generated`）。
- **`-tags`。** 式を満たすタグの組を総当たりで探して `go build -tags` に渡す（タグは 12 個まで）。決して満たされない式は、コマンドライン引数の誤りとして返す。GOOS/GOARCH の項は反映できないので、README に限界として書いた。

1 行目は、生成器のバグとも define ファイルの誤りとも言い切れない。この位置の型エラーは、どちらの可能性もあるからである。そのため、「まず define ファイルのそのフィールドを直し、正しければ生成器のバグ」と順序で示した。

### 9.4 結果

- usecasefuzz の convert-define コーパスは 28 → 34 件で、すべて期待どおり。
  - `c20-fmt-iface`、`c21-claimed-dst`: OK（バグ 1、2 の回帰）
  - `neg04-funclit-sig`: GEN-FAIL（バグ 3）
  - `check01-compute-expr`、`check02-iface`、`check03-named-slice`: BUILD-FAIL として固定。`-check` を付けると converter と field まで戻れる
- `TestRunCheck` で次の 5 つを固定した。成功する場合、型エラーを field に戻す場合、入力が壊れていて判定不能の場合、バグ 1、バグ 2。`-check` のテストは、生成コードの `model` import が解決できるように、convert-define モジュール内の `testdata/` の下に一時パッケージを作ってビルドする。

### 9.5 計画外の遭遇と意思決定

#### D15. `-check` を作る前に、`-strict` の穴とバグを直した

実例を集めたら、`-check` の題材より先に、生成器のバグ（1、2）と構文で分かる穴（3）が出てきた。これらを `-check` で拾えるようにするのは筋が違う。生成時に分かることは生成時に言うべきだからである。`-check` が担当するのは、型検査器がないと分からないもの（任意の式、メソッド集合、ジェネリクス）だけにした。

#### D16. 名前付きスライス同士は直さず TODO にした

`IDs []int` → `Names []string` は、基底型を解決すれば型検査器なしで分かる。ただし正しく直すなら、キャストではなく要素ごとの変換を出すことになり、変換の形を 1 つ増やす変更になる。#49 の範囲を超えるので TODO に回し、コーパスでは BUILD-FAIL として固定した。

#### D17. 既存のテストが、ビルドできない入力を正しいものとして固定していた

§10 の「テストの期待値は、バグも固定してしまう」と同じ型の失敗である。パーサーのテストは、生成されたコードがビルドできるかまでは見ない。DSL の契約（`define.Convert` の doc にある `func(ctx, ec, src) Dst`）を正として、テスト側を直した。

#### D18. usecasefuzz にケースを足した

ユーザーから「不足していたら usecasefuzz に追加しないとダメかも」と指摘があった。issue の前提（「BUILD-FAIL になるケースが候補」）は、コーパスに `-check` 専用の実例がある前提で書かれていた。実際にはなかったので、6 件を追加した。

## 10. 残課題（TODO.md に記載）

- （対応済み）`ConversionPair.Variables`、同じパッケージの識別子との衝突、`-tags` の検証、本番での import 補完の失敗化、provenance の範囲、`-file` の検査、`c.Compute` の型検査（D11）
- （対応済み・§8）DSL の誤用エラーが、1 行目で「誰が直すか」を言わない（D10）
- （対応済み・§9）構文は正しいが型エラーになるコードの provenance。既知の leaf の不一致は `-strict`、それ以外は任意の `-check`（[issue #49](https://github.com/podhmo/minigo/issues/49)）
- 名前付きの合成型同士（`IDs []int` → `Names []string`）をキャストで済ませている（D16）
- import の冗長な alias（`fmt "fmt"`）。省くには本当のパッケージ名が要る（§9.2）

## 11. 「LLM・エージェントに親切なツール」への含意

- **テストの期待値は、バグも固定してしまう。** provenance の誤帰属は、私が自分で書いた golden に入っていた。出力を「見て正しそうなら固定する」golden の運用では、書いた本人の思い込みはそのまま残る。それを見つけたのは、別のエージェントのレビューだった。
- **自動修復を検出する検査は安く入れられて、効果が大きい。** 入れた直後に、何度も隠されていたバグが見つかった。人間にとっての便利さ（goimports が黙って直してくれる）が、エージェントにとっては信号を消すことになる、という指摘の実例になった。
- **失敗の入口は 1 つではない。** 生成器の内部だけを直しても、CLI 引数や入力ファイルの経路に同じ欠点（あとで後段のビルドが失敗する、エラーの省略、エスケープされて潰れた出力）が残っていた。それを見つけたのは、変更の経緯を知らないブラインドのレビューだった。
- **エラーは原因の語彙で返す。** 「生成器のバグであって define ファイルの問題ではない」「`convertSrcToDst` の `Items`」という情報があれば、次にどこを読むべきかが一意に決まる。
- **一つのことに実装を一つだけ。** `frag` 化のあとでは、形状を 1 つ追加・修正するときに触る場所は 1 箇所になった。双子の分岐の片方だけを直してしまうという失敗は、構造上起きなくなった。
