# convert-define: 式/文の二重経路を `frag` に統一する実験

対象: `examples/convert-define/generator`
発端: [issue #48](https://github.com/podhmo/minigo/issues/48)（text/template を使う generator が読みにくい・エラーメッセージから対処が分からない）
ブランチ: `exp/issue48-frag`

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

goimports が補った import を検出する `addedImports` を入れた。本番では WARN を出し、テストでは `strictImports` を有効にして失敗扱いにする。すると既存の integration テスト 2 本が即座に失敗した。

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

## 6. 2 つの見解の検証

| 主張 | 結果 |
|---|---|
| Claude「関数ごとの連番があればスコープ管理は不要」 | **言いすぎ。** 見えている識別子を予約する必要があった。Codex が衝突の例に挙げた `Pair.Variables` は、どの DSL 呼出しからも値が入らない死んだフィールドだったので削除した。同じパッケージの小文字の型名などは、まだ予約していない（残課題）。 |
| Codex「stmts を機械的に追加すると、実行条件や回数が変わりうる」 | **原理としては正しいが、この生成器では起きなかった。** 親が必ず自分のブロックに子を置くので、不変条件を明文化するだけで済み、「どのブロックへ追加するか」という表現は要らなかった。 |
| Codex「テンプレート実行中の副作用は解消済み」 | **一部が誤り。** 上で見たとおり、型名の修飾が残っていた。 |
| 両者「jennifer は要らない」 | 今回の範囲では、そのとおりだった。 |

## 7. 残課題（TODO.md に記載）

- （対応済み）`ConversionPair.Variables` は死んだフィールドだったので削除した
- 一時変数と、同じパッケージの識別子（小文字の型名など）との衝突
- 構文は正しいが型エラーになるコードの provenance（どのフィールド・どの判断から生成されたか）。今回の逆引きは構文エラーにしか効かない。`-strict`（生成時の警告をエラーにする）と合わせて別 issue にする。

## 8. 「LLM・エージェントに親切なツール」への含意

- **自動修復を検出する検査は安く入れられて、効果が大きい。** 入れた直後に、何度も隠されていたバグが見つかった。人間にとっての便利さ（goimports が黙って直してくれる）が、エージェントにとっては信号を消すことになる、という指摘の実例になった。
- **失敗の入口は 1 つではない。** 生成器の内部だけを直しても、CLI 引数や入力ファイルの経路に同じ欠点（あとで後段のビルドが失敗する、エラーの省略、エスケープされて潰れた出力）が残っていた。それを見つけたのは、変更の経緯を知らないブラインドのレビューだった。
- **エラーは原因の語彙で返す。** 「生成器のバグであって define ファイルの問題ではない」「`convertSrcToDst` の `Items`」という情報があれば、次にどこを読むべきかが一意に決まる。
- **一つのことに実装を一つだけ。** `frag` 化のあとでは、形状を 1 つ追加・修正するときに触る場所は 1 箇所になった。双子の分岐の片方だけを直してしまうという失敗は、構造上起きなくなった。
