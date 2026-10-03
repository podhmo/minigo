# gopkg.in/yaml.v3 を reflect ファサードで動かす — 実験レポート（issue #40）

対象: [issue #40](https://github.com/podhmo/minigo/issues/40) (lim-yaml) — `gopkg.in/yaml.v3` の import が `decode.go` の `init()` でトラップする問題。
問い: `unsafe` を忠実に解釈する VM メモリモデル再設計なしに、`reflect` サブセットの **native bind（ホスト委譲）** で yaml が動くか。併せて (a) バインドなしの標準ライブラリソース解釈、(b) GOMODCACHE 経由の外部モジュール解決、(c) 未対応パッケージの**拒否機構**を検証する。

結論: **動く**。`yaml.Unmarshal`/`yaml.Marshal` ともに実動作し、`text/template`・`net/url`・`github.com/BurntSushi/toml` もソース解釈で通った。想定していた「`reflect` ネイティブバインドの実装コスト」よりも、実際は **minigo 本体のセマンティクスバグの方が深い障害**だった — 構造体の値コピー欠落・`copy()` の後方オーバーラップ・埋め込みフィールド解決・型名の変数シャドウ等が順に噛み合っていた。ファサード (`minigo/minireflect`) は host `reflect.Value`/`reflect.Type` とスクリプト値の橋渡し層として ~3100 行で実装できた。

---

## 1. アプローチ — unsafe を解釈せず reflect を委譲する

`reflect.TypeOf` が `internal/abi.TypeOf` 経由で `*(*EmptyInterface)(unsafe.Pointer(&a))` というメモリ再解釈を要求するのが根だった。**unsafe を解釈しない**前提で、新パッケージ `minigo/minireflect` にファサードを実装:

- `RType` — スクリプトの `*runtime.TypeDef` とホストの `reflect.Type` の両方を包むインターン済み型表現（identity が重要な `reflect.Type` 比較用にキーでインターン）。
- `RValue` — ホスト `reflect.Value` または script 側の `(val, ref, td, ro)` 4 要素。`ref` は `*runtime.Cell`/`FieldRef`/`IndexRef`/`DerefRef` で、`Set*` がストア先へ書き戻せる。`ro` は「非公開フィールド由来の値はセット不可」ルール。
- 誤用は `panic(&runtime.Panic{...})` で通知（`reflect` の panic-on-misuse に合わせる）。未対応 API（`NewAt`, `FuncOf`, `StructOf`, `Select`, `Swapper`, `TypeAssert`, `VisibleFields` 等）は**明示的に trap** するスタブを bind しておき、静かに誤動作しない形にした。

配線は `minigo_reflect.go` の `installReflect()` が `e.Bind("reflect", minireflect.Symbols(hooks))` を呼ぶ形。`intrinsics.go` にベタ書きしない要望どおり、intrinsics 側は一行の呼び出しだけ。

## 2. 検証結果

| 対象 | 経路 | 結果 |
|---|---|---|
| `encoding/csv` | `--src` 強制解釈 | `ReadAll`/`Writer`（クォート処理含む）動作 |
| `container/list` | バインドなし素のソース解釈 | `PushBack`/`PushFront`/`Next` 走査 OK |
| `math/big` | 同上 | `SetInt64`/`Mul` 動作（`fmt.Scanner` インターフェース typedef の bind で解決） |
| `github.com/BurntSushi/toml` v1.6.0 | GOMODCACHE + ソース解釈 | 構造体 decode・slice・ネスト・`map[string]any`・`MetaData.Keys()` 全て動作 |
| `gopkg.in/yaml.v3` | GOMODCACHE + ソース解釈 | `Unmarshal`（構造体・map・slice・ネスト）/`Marshal` ともに動作 — **issue #40 のブロッカーは突破** |
| `text/template` | `--src` 強制解釈 | `New`/`Parse`/`Execute`（`{{.Name}}` map 参照・`{{range}}`）動作 |
| `net/url` | `--src` 強制解釈 | `Parse`/`Query`/`Values.Encode`/`ParseQuery` 全て動作 — `internal/godebug` のスタブ bind が必要だった |
| `import "C"` (cgo) | — | `cannot refer to unexported name C.sqrt` でクリーンにトラップ（クラッシュしない） |

## 3. 途中で見つけて直したバグ（minigo 本体側）

この実験の大半はファサード実装より、本体側の「複合リテラル/コピー/名前解決」の穴埋めだった。全てこのブランチで修正済み。

1. **構造体の値コピーセマンティクス欠落** — `key{mark: p.mark}` がパーサ状態をエイリアスして yaml のペアが消失していた。`runtime.Copy` を新設し、`SetRef`/`coerce`/複合リテラルのフィールド代入で深いコピーを挟む。配列・`make`・`append` のゼロ値埋めも `valueCopy` 化（`var a [2]mark` が1つのゼロ Struct を共有するバグも解消）。
2. **`copy()` 組み込みの後方オーバーラップ破壊** — `copy(parser.tokens, parser.tokens[head:])`（yaml の `yaml_insert_token`）で、前向きコピーが src を先読み前に上書きしてカスケード破壊 → 幻の空 SCALAR イベントが混入し "mapping key already defined" に化けていた。スナップショット一時バッファで修正。
3. **`s[*i]` が `*T` 型式と誤分類** — `IndexExpr`/`calleeExpr` が `*ast.StarExpr` を無条件に型式扱いしていたため、`emitting.go` の `width(s[*i])` で TypeDef を index にして落ちていた。`isTypeForm` を `starOperandIsValue` で強化（`resolveName` で変数解決すれば式、型名なら typedef）。
4. **`FieldRef` の埋め込みフィールド解決** — `t.tmpl[k] = v`（`tmpl` が埋め込み `*common` 内）で `FieldRef.Get` が直接フィールドだけを見て失敗 → "index assign on *runtime.FieldRef"。`Def.EmbedIdx` 経由の昇格探索を `Get`/`Set` に追加。
5. **型位置での変数シャドウ** — toml の `case item := <-lx.items: return item`（返り値型 `item` と変数 `item` が衝突）で `return item` の型 coerce が変数値を拾い "declared type is not a type"。`typeExpr` を `typeIdent` に分離し、型位置では `typeDecl` バインディングを優先解決（変数に隠されたパッケージ型は `OpGlobal` で直取り）。
6. **汎用ホスト関数の型引数経路** — `reflect.TypeFor[T]()` は `OpInstantiate` で `BuiltinFunc` に到達しても型引数を渡せなかった。`BuiltinFunc.GenFn` を追加し、インスタンス化時に型引数を束縛したラッパーを生成するようにした（最小侵襲: 既存の call 経路は不変）。
7. **`reflect.Kind` 等の順序比較** — `GoValue` で来る named int 系（`reflect.Kind` 等）が `>=`/`<=` で落ちていた。`smallIntOf` で int64 領域に収まる host 数値をアンラップ。
8. **intrinsic の穴** — `fmt.Stringer`/`GoStringer`/`Formatter`/`Scanner`/`State` のインターフェース typedef、`internal/godebug` の `New`/`Setting` スタブ（`net/url` が ParseQuery 経路で `urlmaxqueryparams` を見るため必須）、`bufio.ErrBufferFull`、`json.Number`、`time.FixedZone`、`sort.Sort`（`Len`/`Less`/`Swap` メンバから scriptSortable アダプタを生成）。

## 4. 拒否機構（拒否フラグ）

`net/url` の「対応は分けても良い」要望に応える形で、パッケージ単位のモード切替を実装した:

- `minigo.WithPackageModes(map[string]PackageMode)` — `ModeAuto`（既定: bind があれば bind、なければソース解釈）/ `ModeSource`（bind を無視して強制ソース解釈 — 実験・検証用）/ `ModeDeny`（import を明示エラーにする）。
- CLI: `minigo run --deny net/url .`、`minigo run --src net/url .`（カンマ区切り・繰り返し可）。
- 動作: `--deny` は `import gopkg.in/yaml.v3: minigo: import of "gopkg.in/yaml.v3" denied by package policy` でクリーンに落ちる（パッケージ名推測パスが `Materialize` のエラーを飲み込んで `undefined: yaml` に化ける問題も併せて修正済み）。
- `net/url` については結論として**ソース解釈で全部動く**ことが分かった（`internal/godebug` スタブ追加後）。ただし既定パスは引き続き「部分 intrinsic bind（エスケープ系のみ）+ 残りソース解釈」になっている — 既定をソース解釈に倒すかは製品判断として残す。

## 5. 未対応・スコープ外

- **cgo**: `import "C"` は `C.sqrt` のような参照で `cannot refer to unexported name` — クラッシュはしないが、cgo 非対応であることがメッセージからは読み取れない（`"C"` を特別扱いして "cgo is not supported" とするかは要検討）。
- **unsafe.Pointer を真に触るコードパス**: `sync/atomic.Pointer` 等の unsafe 起点は依然として `undefined: unsafe.Pointer` で止まる — これは設計どおりのクリーントラップ。
- **`reflect.NewAt`/`StructOf`/`FuncOf`/`Select`/`Swapper`/`VisibleFields`** は facade の明示的 unsupported trap。
- **埋め込みフィールド昇格の同深度曖昧性**: 2つの embed が同じフィールド名を持つ場合に最初の一致を拾う（Go ではコンパイルエラー）。実害は稀。
- **`internal/*` パッケージの bind 戦略**: `internal/godebug` のような「ノブ読み取り専用」の stub は今回手で足した — 同種の stdlib 内部ノブ（`net/http` の `http2client` 等）が今後必要になった場合、スタブ層としてまとめる余地がある。

## 6. Go/No-Go 判断材料

- **手応え**: 高い。yaml・toml・template・url の4系統が「本体セマンティクスの穴埋め + 薄い facade」で通った。unsafe を解釈しない方針は正しかった — 必要だったのは VM メモリ再設計ではなく、構造体コピー・埋め込み・型名解決という通常の言語機能の穴埋めだった。
- **複雑さ**: minireflect 3ファイル ~3100 行（`rvalue.go` が一番大きい）。本質的に複雑なのは「スクリプト値 ↔ host reflect.Value の橋渡しで ref/ro を正しく維持する」ことで、API 表面を増やす作業は機械的。
- **カバー範囲**: 今回の yaml.v3 ・BurntSushi/toml・text/template・net/url は reflect 利用密度が高い部類 — ここまで通るなら一般的なライブラリの大半はカバーできる見込み。`encoding/json` の真のソース解釈も同じ構造（reflect で構造体走査）なので同様に通るはず。
- **残る最悪の壁**: `unsafe.Pointer` を真に計算するコード（`sync.Pool` 内部や `internal/abi` 相当の手書き）だけは原理的に通せない — それは `--deny`/`--src` で明示的に振る舞いを制御すればよい（今回機構を実装済み）。
