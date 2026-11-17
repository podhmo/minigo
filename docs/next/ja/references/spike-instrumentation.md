# スパイクのインストルメンテーション（再現手順）

[../TODO.md](../TODO.md) の M0-3 から参照する。スパイクのコードは使い捨てでコミットしていないので、要点をここに残す。結果は [../../../sketch/ja/spike-rearchitecture.md](../../../sketch/ja/spike-rearchitecture.md) を参照。

## C1 違反とコンパイル起因のビルド

1. `runtime/package.go` の `EnsureReadyRun` と `EnsureConstsRun` で、bootstrap を始める直前にスタックを調べる。`runtime.Stack` の結果に `github.com/podhmo/minigo/compile.` が含まれていれば、C1 違反として記録する。
2. `Engine.buildPackage`（minigo.go）の冒頭で同じ判定を行い、`compile-build <ImportPath>` を記録する。
3. 結果: スイート全体と examples で C1 違反は 0 件。root モジュールでは 2,502 回のビルドのうち 162 回がコンパイル起因で、主に `io/fs`、`mime/multipart`、`net/http`、`net/url`、`math/bits` だった。

常設するなら、`runtime.Stack` を使う方法は遅いので、コンパイラの入口でカウンタを増減し（goroutine ローカルではないので、テスト専用と割り切る）、ビルドタグ `minigo_spike` で有効にするのがよい。

## oracle モード（C3 の検証）

`compile/compile.go` の 8 箇所の `ref.Materialize()` を 1 つの関数に集め、環境変数でモードを切り替えた。

```go
func spikeMaterialize(ref *runtime.ImportRef) (*runtime.Package, error) {
	switch runtime.SpikeOracleMode {
	case "none": // 一切答えない
		return nil, nil
	case "peek": // 他者がすでに materialize した ref にだけ答える
		if p := ref.Loaded(); p != nil {
			return p, nil
		}
		return nil, nil
	}
	return ref.Materialize()
}
```

`ImportRef.Loaded()` は、`once` の中で `done atomic.Bool` を立てて実装した。各モードで `go test ./... -count=1 -json` と全 examples を走らせ、失敗したテスト名を `off` と比較する。差分は [acceptance-set.md](./acceptance-set.md) を参照。

## 既知のノイズ

次のテストは環境が原因で、どのモードでも失敗する。比較からは除外する。

- `TestDiffRegressions/os_err_script_path` と `repl.TestREPLLoadErrors`: サンドボックスが root で動くため、`permission denied` が起きない。
- `TestScriptProfiler*`: 負荷の高いマシンでは時間に依存して不安定になる。
