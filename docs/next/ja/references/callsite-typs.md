# `CallSite` 記述子と実行時に保存する型情報の境界（P4-4b の決着）

[../follow-up.md](../follow-up.md) の P4-4b から参照する。`frame` の旧バッファ（`argbuf`/`statbuf`）は除去済み。残るのは `deferredCall.typs`（`callTyps{statics, spreadTd}`）だけで、ここが「除去対象は重複した静的メタデータか、保存形式も変えるか」の結論を記録する。

## 境界

- **`bytecode.CallSite`（静的、chunk 共有）**: `Expr`/`File`/`Pos`/`Ctx`/`Consts`/`Depth`/`Key`/`After` と、引数ヒント `Args []ArgHint`（recipe 本体）。これらは宣言由来で、1 chunk につき 1 レコード — 全実行で共有される。
- **`callTyps`（動的、登録ごと）**: `statics []*TypeDef` は `Args` の recipe を**登録時点**の呼び出し側 frame に対して解決した結果、`spreadTd` はその場の spread 実引数が運んだ要素 typedef。どちらも「実行時にしか得られない」値。

## 判定: 重複ではなく、形式も現行を維持

`callTyps` を `CallSite` 側へ寄せられない理由は寿命ではなく**多重度**にある:

1. `ArgHint` の recipe は呼び出し側 frame のセル（locals/upvals/globals）を読む。deferred 呼び出しが走るのは frame 解体後で、読み元はもうない — 登録時の解決が必須。
2. 同じ `defer f[T](x)` サイトでも、ジェネリック frame ごとに T の束束縛が違えば `statics` の中身は変わる。結果は per-registration であり、per-site の `CallSite` に置くと共有されて誤る。`spreadTd` も実引数依存で同じ。

つまり `callTyps` は「CallSite の静的メタデータの重複」ではなく「登録時点でしか得られない実行時解決値」で、除去対象にはならない。保存形式（`deferredCall` に `callTyps` を持つ）もこの多重度に対して最小なので、**完了条件は境界の明記**であり、それが本文書。

## 残っている可変要素

- `frame` 側には型情報バッファは残っていない（`argbuf`/`statbuf` 除去済み）。
- `CallSite.Cache` は静的レコード上の1回書きキャッシュで `callTyps` とは別物 — 対象外。
