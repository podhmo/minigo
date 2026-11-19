# 引数のその場渡し（P4-3b の決着）

[../follow-up.md](../follow-up.md) の P4-3b から参照する。通常呼び出しのその場渡し自体は `8aee061`（P4-3b 前半）で実装済み。ここでは残りの引数経路をケース別に見て、どれをその場渡しにできるかを確定する。

## 背景

連続スタックの窓（P4-3a）の上で、`OpCall`（spread なし）は callee スロットを落として呼び出し側の先頭 argc オペランドをそのまま callee のオペランド窓にする — `callInPlace` が argbuf へのコピーを省く。スライスが必要なディスパッチ腕（linkname の receiver 前置、builtin、変換、host 関数）は `inPlaceArgs` が arena の末尾領域をそのまま `[]Value` として見せ、保持が必要な builtin だけが従来通り private copy を取る。

## ケース別の判定

| 経路 | 引数の置き場所 | 寿命 | 判定 |
|---|---|---|---|
| `OpCall` spread なし (`ins.B==0`) | 呼び出し側のオペランド窓 = callee の窓 | 呼び出し中のみ | **その場渡し**（実装済み `8aee061`） |
| `OpCall` spread (`ins.B>0`) | 窓 = 前置オペランド + spread 要素の展開 — スタック上の連続領域と形が違う | 呼び出し中のみ | スライスのまま。展開で要素数が変わるため窓をエイリアスできない |
| `OpDefer` | 登録時に評価、呼び出しは frame 解体時 | **呼び出し側 frame を超える** | コピー必須。窓は frame と共に再利用される |
| `OpGo` | 評価は同期的、呼び出しは別 goroutine | **呼び出し側 frame を超える + 他 goroutine が読む** | コピー必須。arena 末尾は即座に再利用される |
| host callback (`processcaller.spawn`) | host 側から `[]Value` として届く | spawn 先 goroutine | スライスのまま。エイリアス元の窓が存在しない |
| `sched.go` の spawn | 同上 | spawn 先 goroutine | 同上 |

## alloc の比較

- `OpCall`（非 spread）: その場渡しで argbuf コピーが消えた。alloc 数は argbuf が既にプール済みのため変化なし（`8aee061` の BenchmarkGenericCall で確認）— 効果は per-call のコピー除去と窓の形状の統一。
- spread/defer/go: `popArgsCallSite` は `make([]runtime.Value, argc)` に pop する — defer/go は frame を超える寿命が本質的なのでこの確保は不可避。spread は要素展開のため同じく不可避。
- よって「その場渡しにできる経路」は非 spread `OpCall` のみで、残りは寿命または窓形状の理由でスライス経路が正しい — これが P4-3b の確定結果。
