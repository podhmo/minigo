# minigo エラー時トレースバック表示 — 現状調査と改善実験レポート

対象: `podhmo/minigo`（VM ベース再実装版）
比較対象: [PR #3 Traceback experiment](https://github.com/podhmo/minigo/pull/3)（旧 AST インタプリタ `internal/interpreter` 時代の実験）
実験ブランチ: `devin/traceback-experiment` → Draft PR [#17](https://github.com/podhmo/minigo/pull/17)

---

## 1. 現状の仕組み（調査結果）

現行 minigo はスタックベース VM で、すでにトレースバックの骨組みを持っている:

- `bytecode.Instruction` が `Pos token.Pos` を保持（`Instruction.Pos` — `compile` が全命令にソース位置を刻印）
- `bytecode.Chunk` が `Name`（関数名）を保持
- フレーム unwind 時に `vm.trace(f, r)` が `name at file:line:col` 形式の文字列を `Trap.Frames` / `Panic.Frames` に追記（`vm/vm.go`）
- `f.pos()` = 最後に実行した命令の位置 → **呼出元フレームは OpCall の位置 = 呼び出し箇所**が指せる
- フレームは「最新呼び出しが先頭」の順で溜まる（PR #3 の "most recent call first" と同じ）
- パッケージ横断・`main.__init__`・メソッド（`T.M`）・クロージャ（`<funclit>`）も名前が出る
- 未対応構文は設計上 `OpTrap` にコンパイルされ、実行時に到達したときだけ trap する（コンパイル自体は失敗しない方針）
- REPL は `repl.go` という仮想ファイル名で動作する

### 現状の出力例（main ブランチ時点）

```
runtime trap: binary 0 on int64 and bool
G at /tmp/tbtest/main.go:8:9
F at /tmp/tbtest/main.go:4:9
main at /tmp/tbtest/main.go:12:2
```

ファイル名・行・列・関数名はすでに出ている。**PR #3 と比べて欠けているのは「ソース行テキスト」と、いくつかの系統でフレームが完全に欠落する点。**

## 2. エラー種別ごとの実測（修正前）

| 系統 | 例 | 修正前の出力 |
|---|---|---|
| 未対応構文 | `s[0:1:2]` | `runtime trap: 3-index slice is not supported` + フレーム ✓ |
| スクリプト panic | `panic("x")` | `panic: x` + フレーム ✓ |
| 型エラー（演算） | `n + true` | `runtime trap: binary 0 on int64 and bool` + フレーム（演算子が素の数値） |
| nil map 代入 | `m[k]=1` | `panic: assignment to entry in nil map` + フレーム ✓ |
| **ホストpanic: index out of range** | `xs[10]` | `runtime error: index out of range [10] with length 1` — **フレーム・位置なし** |
| **ホストpanic: ゼロ除算** | `1/0` | `runtime error: integer divide by zero` — **フレームなし** |
| **ホストpanic: builtin 内部** | `strings.Repeat(s,-1)` | `panic: strings: negative Repeat count` — **フレームなし** |
| **defer 内 panic** | `defer cleanup()` | `panic: in defer` + cleanup のみ — **defer 登録元フレームが欠落** |
| ジェネリック引数 coerce 失敗 | `Id[int]("s")` | `Id` — **位置なし（裸の関数名）** |
| **再帰によるスタック溢れ** | `func f(){f()}` | **プロセスが fatal error で死亡**、スクリプト文脈なし |
| パースエラー | 構文ミス | `parse <file>: <file>:<line>:<col>: expected operand...`（静的エラー、位置あり） |
| undefined 呼出し | `nosuchfunc()` | `runtime trap: undefined: nosuchfunc` + フレーム ✓ |
| パッケージ init 内 panic | `var x = bad()` | `bad` + `main.__init__` フレーム ✓ |
| パッケージ横断 | import 先で panic | 全フレーム正しいファイル名・行 ✓ |

### 根本原因（ホストpanicでフレームが消える理由）

`vm.exec` の `recover()` が捕捉した値 `r` を `unwind` → `v.trace` に渡すが、`trace` は `*runtime.Trap` / `*runtime.Panic` にしか `Frames` を追記しない。Go ランタイム由来の panic（`b.Elems[i]` の範囲外アクセス、`a/b` のゼロ除算、builtin 内 panic）はどちらでもないので、**フレームを巻き戻しながら通過するのに何も記録されない**まま境界 `v.Call` の `asError` で素のエラー化する。

## 3. PR #3 との差分

| PR #3（旧 AST 版） | VM 版での実現可能性 |
|---|---|
| `File "...", line N, in name()` | **実現済**（`name at file:line:col`、むしろ列も出る） |
| フレーム下にソース行表示（`printer.Fprint` で AST を再表示） | **実現可能**。`Pos`→`Fset.Position`→ファイル名+行を引き、`syntax.File` に `Src` を保持（追加）するか disk から読む。REPL の仮想ファイルも `Src` 経由で表示可 |
| "Traceback (most recent call first)" ヘッダ | 可能だが未実装（`Error()` で付けられる。実験では付けず） |
| tbBuffer による stderr バッファリング | 不要（VM ではエラー値に Frames を持たせる設計の方が自然） |

## 4. 実験ブランチで実施した改善（PoC）

`devin/traceback-experiment`、Draft PR #17。全て小粒な変更:

1. **ホストpanicを `*runtime.Panic` に正規化**（`exec` の recover で `asScriptPanic`）→ 全フレーム記録 + **スクリプトの `recover()` で捕捉可能に**（Go と同じランタイムエラー意味論）
2. **各フレーム下にソース行を表示**（PR #3 スタイル）: `syntax.File.Src` 保持 + disk フォールバック + ファイル名キャッシュ
3. **defer 起源 panic に `(deferred call)` 合成フレーム**: `main (deferred call) at ...:6:2` と登録元を記録
4. **位置のフォールバック**: フレームの `Pos` が無効なら `fn.Decl.Pos()`（宣言位置）を指す + パラメータ coerce に型式の位置を刻印（`token.NoPos` → `typ.Pos()`）→ `Id` 裸フレームが `Id at ...:3:18` に
5. **演算子名表示**: `binary 0` → `unsupported types: int64 + bool`（`BinOp`/`UnOp` に `String()` 追加 — PR #3 と同じメッセージに一致）
6. **`panic(err)` の値表示**: `*GoValue` アンラップで `panic: &{x}` → `panic: x`
7. **スタック深さ上限 10000**: fatal crash を `runtime trap: stack exhausted` + スクリプトフレームに変換（表示は 20 件 + `... and N more frames` に切り詰め）
8. **ホストpanicに Go スタックも保持**: `asScriptPanic` の recover 地点で `debug.Stack()` を採取し `Panic.GoStack` へ。builtin/ホストハンドラ内の panic で Go 側のファイル:行（例: `intrinsics.go:118`）まで表示される
9. 回帰テスト `TestPanicTraceback`/`TestTrapTraceback` + `testdata/traceback/` 追加

### 改善後の出力例

```
runtime trap: unsupported types: int64 + bool
G at /tmp/tbtest/main.go:8:9
		return n + 10 + true
F at /tmp/tbtest/main.go:4:9
		return G(n) + G(n) + 10
main at /tmp/tbtest/main.go:12:2
		F(1)
```

```
panic: runtime error: index out of range [10] with length 1
get at /tmp/tbtest/main.go:4:9
		return xs[i]
main at /tmp/tbtest/main.go:8:10
		println(get([]int{1}, 10))
```

```
panic: in defer
cleanup at /tmp/tbtest/main.go:3:18
		func cleanup() { panic("in defer") }
main (deferred call) at /tmp/tbtest/main.go:6:2
```

## 5. 残っているギャップ / 今後の改善候補

- **builtin 呼出し自身はフレームを持たない**: `strings.Repeat` 失敗時、Go なら `strings.Repeat` がスタックに出るが、minigo は呼出し元のコールサイトのみ。`invokeDeferred` の sentinel 的に OpCall で軽量フレームを積めば出せる（要検討: フレーム積みコスト）
- **ジェネリック表記**: `Id[int]` のインスタンス化がフレーム名 `Id` のまま（Binds から `Id[int]` を再構成可能）
- **callee 側コンパイル失敗**: `EnsureCompiled` 失敗は呼出しサイトの trap になり、失敗箇所ノードの位置がメッセージに残らない（compile error に位置を載せる拡張が要る）
- **`Traceback` ヘッダ等のフォーマット**: 現在は素の `name at file:line` 羅列。PR #3 風 `File "...", line N, in name()` への整形は `Error()` のみの変更で可能
- **引数不足の黙殺**: `add(1)`（本来2引数）が `b=NIL` で動くのは prepFrame の仕様 — 警告/trap にするかは設計判断
- Trap（未対応構文）は recover 不可のまま（設計意図通り。Go ではコンパイルエラー相当なので妥当）

## 6. 範囲外で発見したバグ

| バグ | 状態 |
|---|---|
| `fmt.Errorf("x")` / `Printf("x")` / `Sprintf("x")` が "needs 2 args" で失敗（可変引数なし呼出しが拒否） | **修正済**（`fn2`→`fn1`、最小1引数） |
| `panic(errors.New("x"))` が `panic: &{x}` と構造体ダンプ表示 | **修正済**（GoValue アンラップ） |
| 無限再帰でホストプロセスが fatal stack overflow で死亡 | **修正済**（frame limit 10000 で trap 化） |
| `add(1)` で欠損引数が NIL バインドで黙々と動く（Go ではコンパイルエラー） | 未対応・設計判断要。TODO.md に記録 |
| `Id[int]` がフレーム上 `Id` と出る | 未対応（軽微）。TODO.md に記録 |
| callee 内コンパイルエラーが呼出し位置に归因され失敗箇所の位置が落ちる | 未対応。TODO.md に記録 |

## 7. Round notes: 実際にやってみての想定外

調査・実装を通じて計画時点では読めていなかった発見:

- **ホストpanicは「通過するのに記録されない」抜け道だった。** `trace` は `*Trap`/`*Panic` にしか `Frames` を追記しないので、Go ランタイム由来の panic はフレームを巻き戻しながら素通りしていた。`exec` の recover 地点で `*Panic` に正規化する一本の変更で、index out of range・ゼロ除算・builtin 内 panic・defer 内 panic が一括で治った。箇所別に潰すのでなく境界で正規化するのが正解だった。
- **`debug.Stack()` は recover 後でも panic 起点を含む。** deferred call の実行中は panic を起こしたフレームが論理スタック上にまだ生きているため、recover 地点で撮ったスタックに builtin・ホストハンドラ側の file:line が残る。PR #18 で欲しかった「interpreter.go の何行で nil deref したか」はこれで完全に取れた。スクリプトフレーム表示とセットで出るので、DSL 側の呼出し位置と Go 側の障害位置を一枚のエラーで読める。
- **位置情報は想定よりほぼ出来上がっていた。** 「VM ベースで同じものができるか」は杞憂に近く、全命令に `Pos` が刻印・`Chunk` に関数名・caller フレームは `Code[ip-1]`（OpCall）で呼出し位置が自然に指せる構造があった。欠落は `token.NoPos` emit や Decl fallback のような個別経路だけだった。
- **defer 起源の panic は「誰が defer したか」が完全に消えていた。** panic した deferred 関数のフレームだけで登録元フレームが出ないので、`main (deferred call) at ...` の合成エントリを `runDefers` の recover で付けた。これがないとスタックが途中で切れたように見える。
- **再帰でプロセスごと死んだ（fatal stack overflow）。** VM フレーム数に上限がなく、スクリプトの無限再帰がホストのスタックを食い潰す。frame cap で `runtime trap: stack exhausted` に変換。Go でもスタック枯渇は recover 不可なので Trap（recover 不可）は Go 意味論に忠実 — ただしレビューで「recover 可能にすべきか」は設計判断として残っている。
- **`FileByName` のキーと `Fset.Position().Filename` が同一文字列。** 「パッケージが読み込んだファイルに限定する」ゲートがファイル名比較だけで実現でき、AllowedRoots を再解釈しなくても任意ファイル読取を塞げた（Devin Review のセキュリティ指摘への対応）。
- **`fmt.Errorf("x")` が動かなかった（範囲外バグの実例）。** 確認用に回したら `needs 2 args` で失敗 — 可変引数0個の呼出しが `fn2` arity で拒否されていた。エラー表示を試す行為自体が潜伏バグを掘った。
- **`add(1)` が黙々と動く。** Go ではコンパイルエラーなのに `prepFrame` が欠損引数を NIL bind する。意図的な緩さとも読めるので修正せず残件に記録（設計判断要）。
- **表示上限の shape はレビューで変わった。** 当初 20 件先頭のみだったが、上限10000フレームに対し「1000程度に収めて head/tail 省略」が要望として出て、発生箇所側500 + elided + main 側500 の形に落ち着いた。確認済み: 700 件では省略分岐に入らず全件そのまま出る。

## 8. 結論

- **ファイル名・関数名・行・列はすでに出ていた**。VM 化で「同じものができるか」は → **できる。命令に Pos、Chunk に Name、unwind 時に Frames 集約、という構造がすでにあった**
- **PR #3 の `File "..." line N in f()` + ソース行の表示はほぼ再現可能**（ソース行は実装済。ヘッダ形式の差は整形のみ）
- 最大の実質ギャップは**ホストpanic系でフレームが全欠落**していた点（exec 境界で正規化すれば一括で治る）と、**再帰でのプロセス死亡**（frame cap で trap 化）
- トレードオフ: ホストpanic を `*Panic` にすると recover() で捕まえられる（Go忠実）が、VM 内部バグ由来の panic も script-catchable になる点だけ注意（報告用に `*Trap` 化して recover 不可を保つ選択肢もある — Go 意味論的には Panic が正しい）
