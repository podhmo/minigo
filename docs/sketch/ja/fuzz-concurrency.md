# 並行処理の網羅的ファジング実験 — バグ発見と修正のレポート

対象: `podhmo/minigo` の PR #27（ホスト goroutine による真の並行処理）
方法: 仕様上あり得る使い方を「思考を頼りに」系統立てて投入する、手作りの PBT/fuzzing。基盤となった `docs/sketch/ja/experiment-concurrency.md` のテストはハッピーパス中心だったため、境界・失敗・合成の組み合わせを重点的に叩いた。
ブランチ: `devin/1790891542-concurrency-fuzz`（base は PR #27 の `devin/1790851567-concurrency`）

---

## 1. ハーネス

`~/concfuzz/` に手作りハーネスを構築した:

- `cases/<theme>/main.go` — 1 ディレクトリ = 1 テーマ、exported な `func X() int` をケース単位で列挙
- `run.sh <dir>...` — `timeout 8 ./minigo ./cases/<dir> <Func>` を全ケースに実行。panic / デッドロック / 無応答を終了コードで区別（exit 0=正常値、1=script panic、2=host fatal、124=タイムアウト）
- `host/main.go` — `minigo.NewEngine` を直接使うホスト側検証（`ImportRef.Materialize`、同時 `e.Run`、goroutine 数の実測）。`-race` ビルドでデータ競合も観測した

ケース群: `once` `chanofchan` `chan_kind` `close_cases` `deadlock` `defer_exit` `gspawn` `loopvar` `misc2` `neg` `rangex` `recvbinds` `sel_misc` `selassign` `sync2` `time2` `timeriso` — 合計 ~80 関数。

オラクルは Go 本家の仕様（go/doc/effective_go・言語仕様）を手で当てはめる方式。`// want` コメントに期待値を書き、panic/致命傷/無限待ちも「期待される挙動」として比較した。

## 2. 発見したバグ（修正済み）

### B1. `sync.Once.Do` が呼べない — func 型引数の変換が未実装

`o.Do(f)` → `cannot use *runtime.Closure as func()`。`selectMember` の GoValue メソッド呼び出しは引数を `toReflectValue` で reflect.Value に変換するが、**func 型パラメータのアダプタが存在しなかった**。`sync.Once` は bind されているのに `Do` が一切使えない状態だった。

修正: `toReflectValue` に `vc runtime.VMCaller` を通し、func 型ターゲットでは `reflect.MakeFunc` でスクリプト callable をホスト func に適合させる `adaptFunc` を追加。コールバックは呼び出し側 VM に戻る（`sync.Once.Do` のように同期的に呼ぶ分には正しい goroutine）。

### B2. チャネル送信でポインタ同一性・コンテナが失われる

`ch <- &n`（`chan *int`）→ 受信側で `*p` が `deref of non-pointer int64`。`ch <- []int{...}` / `map` → `cannot convert script slice/map to interface{}`。

原因: `OpSend`/`OpSelArm` は `toReflectValue(val, elemT)` に渡すが、スクリプトチャネル（`chan runtime.Value`）の elem は空 interface なのにパススルーがなく、先頭の `Deref` ループが `*Cell`（ポインタの表現）を剥がして `int64` を送っていた。Slice/Map は「interface{} へ変換不能」で弾かれていた。

修正: `toReflectValue` の先頭で `t.Kind() == Interface && NumMethod() == 0` の場合は **unwrap 前の生の runtime.Value をそのまま interface に入れる**。Cell はポインタとして保存され、Slice/Map/Func/Struct も素通りする。`ch <- &n` → 受けて `*p = 9` → 送信元の `n` が更新される（`ChanPointerIdentity`）、`chan []int`/`map[K]V`/`any` が全て通る。

### B3. `time.Duration` 定数が 0 として読まれる

`time.NewTicker(time.Millisecond)` → "non-positive interval"、`time.Sleep(time.Hour)` が瞬時に返る、`select` の `time.After` アームが即時発火（`AfterInLoopSelect` が 0 を返した）、`2 * time.Second` が `unsupported types: int64 * time.Duration` で trap。

原因: bound な `time.Second` 等は生の `time.Duration` 値として渡るが、`durOf`→`intOf` が `time.Duration` の case を持たず 0 を返していた。`binaryOp` も `time.Duration` を int64 に正規化しなかった。

修正: `intOf` に `case time.Duration` を追加し、`binaryOp` で Duration オペランドを int64 に正規化。`time.Sleep`/`After`/`NewTimer`/`NewTicker` と `d1 + d2`、`d < timeout`、`2*time.Second` が全部動く。

**重要な副作用**: 既存テストは全部 `synctest` のフェイククロック内で動いていたため、Duration が実質 0 ns でもテストは通っていた — ハッピーパスがバグを隠蔽していた典型例。

### B4. 空の `default:` 本体で select がデッドロック

`select { case <-t.C: ...; default: }`（本体が空）→ ホストの "all goroutines are asleep" 致命傷で終了。`compile/compile.go` の `selectStmt` が `defaultBody = clause.Body` として `defaultBody != nil` で `hasDefault` を判定していたため、**空本体の default が「default なし」に化けていた**。

修正: `clause.Comm == nil` を見て専用の `hasDefault` フラグを立てる（本体の有無に関わらず default アームを emit）。

### B5. `for k, v := range ch` が受理され `(v, v)` を yield

Go はコンパイルエラー（"range over ch must have at most one iteration variable"）。minigo では `iterNext` の 'c' ケースが `push(val, val)` で 2 変数を黙って受理していた。

修正: `iterNext` 'c' で `nvars == 2` なら `runtime trap: range over channel allows at most one iteration variable`。

### B6. procExit アンワインド中に defer が打ち切られる

プロセス終了でゴルーチンがパークを解除される際、**defer 内の呼び出しが再びパークすると、そのフレームの残りの defer が全てスキップされた**（実測: `C` のみ表示、`B`/`A` 欠落）。`runDefers` が `default:`（procExit 含む）を再 panic して残りを捨てていた。

修正: `procExit` を `case procExit:` で分け、panic せず残りの defer を drain（Goexit が defer を drain するのと同じ）。`DeferChainOnExit` が `C\nB\nA` を全部出すようになった。

### B7. `sync.Once` 然り、イントリンシックのコールバックが engine の VM 上で動く

`sort.SliceStable`/`Search`/`SliceIsSorted`、`slices.BinarySearch`/`BinarySearchFunc`/`EqualFunc`/`IndexFunc` 等が `h.v.Call(...)`（= `e.vmm`）でスクリプトのコールバックを呼んでいた（`filepath.WalkDir` は正しく `v` を使っていた）。**spawn された goroutine 内から呼ぶと、コールバックが呼び出し元ではなくエンジンのルート VM 上で実行され**、proc スコープがずれる（コールバック内のブロッキングが proc.done を見ない、panic の帰属が別 run に行く等）。

修正: 各 `BuiltinFunc.Fn` の第一引数 `v runtime.VMCaller`（呼び出し元 VM）を使うよう `h.v` の参照を全て `v` に置き換え、`hostHelpers.v` フィールドを削除。`sortSlice` メソッド値も `_` パラメータを `v` に。

副発見: `"Slice": h.sortSlice` は `BuiltinFunc` で包まれていない生メソッド値で、`sort.Slice` は **呼ぶと `is not callable` で常に死んでいた**。`&runtime.BuiltinFunc{Fn: h.sortSlice}` で包んで修正。

### B8. 同時 `e.Run` が `e.vmm` でデータ競合

2 goroutine から同じ `Engine` の `Run` を呼ぶと `-race` が `EnsureProc` の `v.proc` 読み書きを検出（加えて片方の `ReleaseProc` がもう片方の proc.done を close して殺す論理バグ）。`e.vmm` という共有 VM が根本原因。

修正: `e.Call`/`EvalExpr` が呼出ごとに `e.newVM()` で新規ルート VM を作るよう変更。`e.vmm` フィールドと `initRunner` を廃止し、lazy init は呼出の runner クロージャに渡す（従来どおり run の proc スコープに属する）。`-race` で警告が消えた。

### B9. `for i := 0; i < N; i++` のループ変数がイテレーション間で共有される

goroutine で発見: `for i := 0; i < 3; i++ { go func() { ch <- i }() }` が `0+1+2=3` ではなく `3+3+3=9`。Go 1.22 以降は反復ごとに新しい変数が作られるが、minigo はセルをループの外で1つだけ確保していた（goroutine に限らず、クロージャでも `fs[0..2]()` が全部 3 を返した）。

修正: `forStmt` で `:=` 形式の init が宣言する名を、ループ本体の先頭で `OpLocal outer + OpNewLocal shadow` により **反復ごとの新セルに shadow** し、本体後に値をキャリアセルへ書き戻す（post/cond が本体での書き換えを見る Go セマンティクスを保持）。`range` 系は `OpRangeNext` が既に反復ごとにセルを作るので対象外だった — `for i := range N` や `for _, v := range xs` は最初から正しかった。

### B10. `ImportRef.Materialize` の panic で以後の呼び出しが `(nil, nil)`

`Load` が panic すると `sync.Once` は消費済みだが `r.pkg`/`r.err` が nil のまま残り、2 回目以降の `Materialize` は `(nil, nil)` を返して下流で nil deref。

修正: `once.Do` 内の defer recover で `r.err = fmt.Errorf("import %s: %v", ...)` を記録してから再 panic。再 panic は初回呼び出しの診断を残し、後続はエラー値を返す。

### B11. init の panic でパッケージが `Initializing` に張り付く（理論的な強化）

`runBootstrap` の deferred recover は `err` に値を入れて再 panic するが、`initOnce.Do` の内側なので `p.initErr`/`SetState(Failed)` に届かない。実際には `vmm.Call` がスクリプト panic を error として返すため発生経路はほぼ host panic/procExit 限定だが、到達した場合は後続のメンバアクセスが `Initializing` の部分初期化パッケージを黙って供給していた。

修正: recover で `p.initErr = err` + `p.SetState(Failed)` を先に記録してから再 panic（コメントに書かれていた意図そのもの）。

## 3. Go と同じで「バグではない」確認済み挙動

- `sync.Mutex` の未ロック `Unlock` → host の `fatal error: sync: unlock of unlocked mutex`（Go と同じ）
- 全 goroutine がパークすると host の `fatal error: all goroutines are asleep - deadlock!` でプロセス終了（root の self-deadlock、mutex 相互ロック、`select {}`、交差送信デッドロックの全部で発火）
- `go x`（非呼び出し式）→ `expression in go must be function call` でパース拒否 — Go と同じ文法レベル
- `close` の二重呼び出し・closed への送信は recover 可能な panic、`select` の `case ch <- x` では全ケースのオペランドがソース順に評価される
- `select` で `x = <-ch` の代入先が `s.field`/`a[i]`/`m[k]`/`_` いずれも正しく動く
- `chan`/`Named`/`IfaceNil`/`TypedNil`/`GoValue`（`time.After`）のチャネル解決、`chan chan T`/`func`/`struct`/any/名前付き chan/構造体フィールドの chan が全部通る
- ネスト select、ループ内 select、goroutine 内 select、`if`/式オペランド内 select、`v, ok := <-ch` の 2 値受信
- goroutine の panic/trap は root を `select {}` でパークしていてもプロセス全体を殺し、Run が `panic: ...` を返す
- `WaitGroup`（goroutine 内 Wait、コピーはホストポインタを共有するため参照先は同一）、`Mutex`（構造体フィールド・ポインタメンバ経由）、`RWMutex`、`Once`、`Timer`/`Ticker` の Stop/Reset
- proc kill で解放されるのは select 型ブロッキングのみ — `chan` 送受信と `select` は `proc.done` アーム付き `reflect.Select` で解除される（`DetachedLeak` のチャネルパークは goroutine 数が戻ることを実測）

## 4. 見つかったが修正しなかったもの（既知の限界）

- **ホスト呼び出し内でパークした goroutine は漏洩する**: `wg.Wait()`/`Mutex.Lock()`/`time.Sleep` のような「select ではない」ホスト呼び出し内でブロックした spawn ゴルーチンは `proc.done` を監視しないため、root が返っても残る（`DetachedWait` — 実クロックで goroutine 数 +1 を確認、`TestHostParkLeak` で記録）。synctest バブル内では "blocked goroutines remain" として検出されるため synctest では試せない。Go で `go func(){ wg.Wait() }()` が同様に残るのと同じ意味での制限であり、任意のホスト呼び出しをキャンセル可能にする汎用的な方法はない。
- **デッドロックはプロセスを殺す**: 全 goroutine のパークで host ランタイムが fatal を出す — CLI としては Go と同じ挙動だが、minigo を組み込みで使う場合はホストプロセスごと落ちる（= 「ハングする」のではなく「落ちる」）。
- **`MakeFunc` で包んだコールバックをホストが別 goroutine から呼ぶケース**: `sync.Once.Do` のような同期呼び出しは安全だが、ホストがコールバックを保持して非同期で呼ぶ形（例 `time.AfterFunc` は現在未 bind）だと、呼ばれた VM は goroutine 安全でないので危険。`adaptFunc` のコメントに明記した。
- **`runDefers` 中の `*runtime.Trap` は残りの defer を捨てる**（従来仕様 — Trap は「実行不能」を意味するので意図的。ただし panic と Trap の区別がちょっと強い）。

## 5. 試行錯誤で得た知見

1. **synctest は便利だがフェイククロックがバグを隠す**。Duration 系（Sleep/After/Timer/Ticker）が全部 0 ns に見える世界では「即時発火しても正しい動作に見える」。実クロック・実時間のケース（`timeriso`）と、算術レベルの検証（`2*time.Second != 0`）を混ぜる必要があった。
2. **`proc.done` アームの有無が「goroutine が死ねるか」の分岐点**。チャネル系 op は全部 done 付き select になっていたおかげで「生き残る goroutine パーク」はホスト呼び出し系に限定できた。これはアーキテクチャ上の正しい設計。
3. **`h.v`（= e.vmm）のような「束縛時の VM」パターンが危険**。bound builtins が呼び出し側 VM ではなくエンジン VM を覚えていると、spawn 後の呼び出しが別 proc で動く。`BuiltinFunc.Fn` の `v runtime.VMCaller` 引数は正しい窓口 — それを無視する書き方が一箇所残るだけで壊れる。
4. **`chan runtime.Value` の要素型は `interface{}`**。`toReflectValue` の空 interface パススルーがないと、ポインタ同一性（Cell）もコンテナも失われる。「script チャネルの elem は interface{}」という前提が散在していた。
5. **`sync.Once` 内の `once.Do` は panic で消費済みになる**。`Materialize`/`runBootstrap` のような once 越しの失敗記録は、panic を握る側で err に書き戻さないと後続が「成功扱い」になる。
6. **`for` ループの反復変数は Go 1.22+ で「反復ごとに新しい」** — range 系は既に対応済みだったが 3 節 for だけ残っていた。goroutine 実験が一般意味論のバグを炙り出した形。

## 6. 回帰テスト

`testdata/concurrency/main.go` に修正対象の再現ケースを追加し、`concurrency_test.go` の `TestConcurrencyBlocking` 表に登録:

`OnceDo` `ChanPointerIdentity` `ChanSliceSend` `ChanMapSend` `SelectEmptyDefault` `DurationArithmetic` `LoopVarPerIteration` `SortInGoroutine`（+ `RangeChanTwoVars` は `TestRangeChanTwoVars`、`DetachedWait` は `TestHostParkLeak`）

`sort.Slice` が実は呼べなかった件、`sync.Once.Do`、Durations、select 空 default、`chan *int` のポインタ同一性、`for` 反復変数 — 全部このラウンドで発見・修正された。
