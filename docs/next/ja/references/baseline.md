# ベースライン

[../TODO.md](../TODO.md) の M0-1、M0-2 から参照する。

## スパイクでの計測値（main `a3f4e24`、4 CPU、ノイズの多い共有マシン）

| ベンチマーク | time/op | B/op | allocs/op |
|---|---|---|---|
| `installStdlib` 単体 | 0.76–0.87 ms | 425 KB | 4,203 |
| `NewEngine` | 6–11 ms | 566 KB | 4,437 |
| `NewSession` | 0.57–1.0 ms | 440 KB | 4,273 |
| `NewEngine` 経由の最小の実行（`println(1)`） | 11–14 ms | 593 KB | 4,655 |
| `NewSession` 経由の最小の実行 | 1.1–1.4 ms | 451 KB | 4,429 |

- `NewEngine` の時間の大半は、`resolve.NewGoScanResolver` → `locator.getGoRoot`/`getGoModCache` が起動する `go env` のサブプロセスで占められている（pkg/locator/locator.go:133–138, 474–490）。
- `NewSession` では、`installStdlib` が実行時間の 60〜70%、アロケーションの約 94% を占める。
- `installStdlib` にはキャッシュがない。Engine ごと（minigo.go:270）にも Session ごと（minigo.go:455）にも呼ばれる。

## M0-2 で記録する項目

| 項目 | 取り方 | 値 |
|---|---|---|
| `NewEngine` / `NewSession` | 使い捨てでない `Benchmark*` を追加する | 未記録 |
| `session_bench_test` | `go test -bench . -run ^$ -benchmem` | 未記録 |
| `genericbench_test` | 同上 | 未記録 |
| realworld `grafana-openapi` の実行時間と alloc | podhmo/minigo-usecasefuzz の `realworld/` | 未記録 |
| コンパイル起因のビルド率 | [spike-instrumentation.md](./spike-instrumentation.md) | スパイク時 6.5%（162/2,502） |
| `chunkCellsEscape` が true の chunk の割合 | [slot-classification.md](./slot-classification.md) | スパイク時 44.5% |

以降の PR では差分を報告し、10% を超える劣化には PR で理由を書く。
