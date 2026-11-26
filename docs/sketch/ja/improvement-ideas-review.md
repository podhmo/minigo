# improvement-ideas.md 精査メモ（PR #1438）

対象: [PR #1438](https://github.com/podhmo/minigo/pull/1438) `docs: add owner-perspective improvement ideas sketch`。
2026-11-26 時点の PR 本文・差分・GitHub コメントを確認し、main（027bae31）のコードと照合した。

## コメント状況

取得時点では issue comments / review comments / reviews はいずれも 0 件だった。したがって精査対象は PR 本文と `docs/sketch/ja/improvement-ideas.md`・`TODO.md` の差分であり、外部レビュー指摘への回答はない。

## 結論

- 方向性はおおむね妥当。特に **P1（`retypeStore`）** と **C2（root 環境テスト）** は小さく切り出して進めてよい。
- 初期版にはいくつか古い前提・事実誤認があった。`improvement-ideas.md` 本体はこの PR 内で修正済み。
- 未決事項の管理場所は既に [`docs/open-questions.md`](../../open-questions.md) として存在した。新規作成ではなく、今回のふんわりした論点をそこへ追加した。
- TODO.md には「 actionable になったもの」だけを足した。判断待ちのものは open questions、測定待ち・優先度が低いものはこのメモに留める。

## 独立に確認した事実

このセッションで別 worktree / 一時スクリプトを使って確認した値。厳密な CI 用ベンチではなく、PR 内容の sanity check。

| 確認対象 | 結果 |
|---|---|
| `fmt.Println("x")` の起動 | 20 回中央値 **11ms**（原文の 13ms と同じオーダー） |
| P1 の 5M ループ | base 中央値 **2.371s**、`sameBareScalar` 前倒し patch 中央値 **1.672s**（各 5 回）。原文の 2.34s → 1.86s と同じ方向。difffuzz / strict lazy conformance は未実行 |
| `minigo vet` に `undefinedThing()` | 出力なしで **exit 0** を再現。実行すると `runtime trap: undefined: undefinedThing` |
| panic traceback / exit | 絶対パス・行のみ・列なし、直接バイナリで **exit 1** を再現 |
| shebang idiom | `--` なしでは script 引数を minigo positional arg と解釈して **exit 2 / unexpected arguments**。`--` ありなら `os.Args` に script 引数が正しく入る |
| `.Elems` 直接参照数 | test を除く `.go` ファイルで **約 313 箇所**（原文の ~280 は概数としては近いが更新した） |

## 初期版から修正した点

1. **P3 は前提が古かった。** frame は既に goroutine ごとの連続 operand stack `v.vals` の `(base, sp)` 窓であり、`sp` インデックスも存在する。`bytecode.Verify` は `MaxStack` を計算するが返り値を保持していない。したがって「連続スタックを導入する」ではなく「導入済み連続スタックの拡張コストを `MaxStack` で削る」問題に修正した。
2. **F2 の根拠が違った。** `examples/test-detect` は affected test detection の実験で、テスト実行基盤ではない。直接の証拠は `tools/tmpltests` の generated driver と最小 `testing` shim。
3. **F4 は `--` が必要。** 初期版の `///usr/bin/env minigo "$0" "$@"; exit` では script 引数が entry function / extra positional arg と解釈される。`--` を入れた形に修正した。
4. **U5 の継続行は実装済み。** `repl.IncompleteInput` と `.. ` prompt がある。残りは `:type` / `:time`。
5. **F1 の vet 説明は狭すぎた。** vet は stub call だけでなく `os.ReadFile` performance hint も出す。ただし eager compile による `OpTrap` 列挙はない。
6. 表内の参照ミス（root test は C4 ではなく C2）を修正した。

## 項目ごとの判定

| ID | 判定 | 置き場・理由 |
|---|---|---|
| P1 `retypeStore` | **採用** | TODO 追加済み（PR 初期版）。独立計測でも効果を確認。difffuzz / strict lazy conformance を通してから実装 PR にする |
| P2 builtin typedef cache | **採用（P1 後）** | P1 の TODO 内に follow-up として記載済み。P1 適用後の profile で効果を再確認する |
| P3 `MaxStack` | **修正して採用候補** | TODO 追加済み。連続スタックは済んでいるため、残コストを再計測してから。実装時は per-frame stack に戻さない |
| P4 superinstruction | **保留** | もっとも効く箇所が P1/P3 の適用後に変わる可能性が高い。まず再計測。tagged `Value` 自体は既存判断でも見送り気味 |
| P5 benchmark CI gate | **open question** | ベンチ集合・閾値・CI コストが未決定。[`docs/open-questions.md`](../../open-questions.md) `perf-gate` |
| P6 compact `[]byte` | **既存 open question を更新** | readfile-mem は既に open question。PR の段階導入案を「推奨案」として追記したが決定ではない |
| C1 budgets ratchet | **既存 TODO** | #1396 に既にある。今回の「上限 + 片方向ラチェット」は有力な具体案だが重複起票しない |
| C2 root test guard | **採用** | TODO 追加済み（PR 初期版）。root container で clean checkout の `make test` が赤い問題は実害がある |
| C3 trap codes | **採用** | TODO 追加済み。ただし taxonomy を広げすぎると運用が重いので最初は少数の code に絞るべき |
| C4 巨大ファイル分割 | **既存計画** | rearchitecture P1 に乗せる。性能改修と同時にやらない、という順序注意は妥当 |
| F1 `minigo check` / `vet --deep` | **open question** | 価値は確認できたが、公開コマンドにするか vet の深い mode にするかは未決定。`check-command` に記録 |
| F2 `minigo test` | **open question** | `tools/tmpltests` が部分実証だが、公開コマンドとしての最低互換範囲は未決定。`test-command` に記録 |
| F3 `--watch` | **保留** | pitch に合うが、polling / incremental invalidation / process 再実行の UX を決めないと半端なものになりやすい。今回は TODO に入れない |
| F4 shebang | **採用** | `--` 修正済み。document + regression test の TODO を追加 |
| F5 `CallAs[T]` | **open question** | scalar だけなら浅い helper、任意型なら deep conversion contract が必要。`typed-call-api` に記録 |
| U1 traceback | **採用** | TODO 追加済み。`path:line:col`、相対パス、stdlib fold はデバッグ体験に直結 |
| U2 did-you-mean | **採用** | U3 とまとめて TODO 追加済み。error path のみでコストを払う設計にする |
| U3 next-action hints | **採用** | U2 とまとめて TODO 追加済み。C3 の code と組み合わせると explain に繋げやすい |
| U4 exit code | **open question** | compiled Go parity、`go run` 互換、trap 分離のどれを public contract にするか未決定。`exit-codes` に記録 |
| U5 REPL `:type` / `:time` | **保留** | 継続行は実装済み。残り 2 つは便利だが実ユースケースが弱いので TODO には入れない |
| U6 help examples/env | **採用** | TODO 追加済み。小さく、初見 UX に効く |

## open questions に追加したもの

既存の [`docs/open-questions.md`](../../open-questions.md) に、今回の未決事項として次を追加した。

- `exit-codes` — panic / trap / `os.Exit(1)` をどう区別するか
- `check-command` — deep trap 列挙を `minigo check` にするか `vet --deep` にするか
- `test-command` — `minigo test` を公開機能にするか、最低限の `testing.T` 互換をどこまでにするか
- `perf-gate` — どの性能契約を CI で強制するか
- `typed-call-api` — `CallAs[T]` の変換契約を浅くするか深くするか
- `readfile-mem` — 既存項目に compact `[]byte` 段階導入の推奨案と `.Elems` 数の更新を追記

## 次に進める順序の提案

1. **P1** を実装 PR にする。difffuzz regression corpus と strict lazy conformance を通す。
2. **C2** を独立 PR にする。root container での clean checkout を緑にする。
3. **U1 / U2+U3 / U6 / F4** は独立した小さい UX PR に分ける。
4. **C3** は U2/U3 の前か後に入れるとエラー改善を進めやすい。
5. open questions は podhmo 判断後に TODO へ昇格する。
