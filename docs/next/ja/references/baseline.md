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
| `NewEngine` / `NewSession` | 使い捨てでない `Benchmark*` を追加する | 下の表（`d3584e9`、M0-1 適用後） |
| `session_bench_test` | `go test -bench . -run ^$ -benchmem` | 下の表 |
| `genericbench_test` | 同上 | 下の表 |
| realworld `grafana-openapi` の実行時間と alloc | podhmo/minigo-usecasefuzz の `realworld/` | 下の表 |
| コンパイル起因のビルド率 | [spike-instrumentation.md](./spike-instrumentation.md) | スパイク時 6.5%（162/2,502） |
| `chunkCellsEscape` が true の chunk の割合 | [slot-classification.md](./slot-classification.md) | スパイク時 44.5% |

以降の PR では差分を報告し、10% を超える劣化には PR で理由を書く。

## 記録値（`d3584e9`、M0-1 適用後、8 CPU、linux/amd64、`-count=5`）

`go test -run '^$' -bench <name> -benchmem .` の中央値レンジ。

| ベンチマーク | time/op | B/op | allocs/op |
|---|---|---|---|
| `BenchmarkNewEngine` | 0.49–0.62 ms | 486 KB | 4,518 |
| `BenchmarkNewSession` | 0.47–0.56 ms | 468 KB | 4,434 |
| `BenchmarkSessionForkReload/PackageOnly` | 1.39–1.77 ms | 680 KB | 5,064 |
| `BenchmarkSessionForkReload/PackageAndCall` | 1.52–1.58 ms | 708 KB | 5,394 |
| `BenchmarkReloadStages/LocateDir` | 0.37–0.42 ms | 126 KB | 490 |
| `BenchmarkReloadStages/Parse` | 1.07–1.19 ms | 936 KB | 24,294 |
| `BenchmarkReloadStages/Index` | 0.54–0.63 ms | 498 KB | 4,480 |
| `BenchmarkReloadStages/Session` | 0.56–0.61 ms | 468 KB | 4,433 |
| `BenchmarkGenericCall/RunPlain` | 93–120 ms | 8.0 MB | 599,493 |
| `BenchmarkGenericCall/RunInferred` | 170–210 ms | 37–43 MB | 1,799,540 |
| `BenchmarkGenericCall/RunExplicit` | 186–207 ms | 44 MB | 2,099,555 |
| `BenchmarkGenericCall/RunPartial` | 317–325 ms | 97 MB | 3,499,880 |
| `BenchmarkGenericCall/RunMethod` | 304–306 ms | 355 MB | 2,900,204 |

realworld `grafana-openapi`（podhmo/minigo-usecasefuzz `realworld/run.sh grafana-openapi`、minigo は `d3584e9` のビルド、PASS）: minigo 側 0.08–0.09 s、`MINIGO_MEMPROFILE` の alloc_space 合計 24.60 MB。oracle なし（`want.txt` golden のタスク）。
