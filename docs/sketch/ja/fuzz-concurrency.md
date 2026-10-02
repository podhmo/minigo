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

---

## 7. 計画外: `sync` パッケージ実装ラウンド（ユニットテスト先行 + fuzz 追撃）

PR #27 で「並行動作を実装した」と言いつつ、`sync` パッケージは WaitGroup/Mutex/RWMutex/Once の 4 型が bind されているだけで実用にならなかった（Map/Pool/Cond/Locker/NewCond/OnceFunc 系が全滅、`sync.Map` の API は型ごと未定義）。ユーザの指摘を受け、**まずユニットテストで動作確認を固めてから fuzz corpus を再構築して追撃**した。

### 7.1 順序: ユニットテストを先に書く

`testdata/concurrency/main.go` に `Sync*` 系の関数を先に追加し、`TestConcurrencyBlocking`（synctest バブル + `-race`）で回した。fuzz より先に green を取る方針は正しかった — 実装の主要な欠落（下記 S1-S6）は全部この段階で炙り出せており、fuzz 側の新規発見は埋め込み昇格（S5）だけだった。

追加した関数: `SyncMapBasic` `SyncMapOps` `SyncMapConcurrent` `SyncPoolNew` `SyncPoolDecl` `SyncPoolConcurrent` `SyncCondSignal` `SyncCondBroadcast` `SyncScriptLocker` `SyncRWMutex` `SyncLockerDecl` `SyncTryLock` `SyncOnceFunc` `SyncOnceValue` `SyncOnceValues` `SyncWaitGroupGo` `SyncMethodMutex` `SyncEmbedMutex` `SyncEmbedPoolField` `SyncAssertHostPtr`。

注意点: `sync.Pool` の Put→Get の同一性は本家 Go でも保証されない（P 移行・GC で消える）ため、テストは `New` の dispatch と型だけを assert する。当初「Put した値が取れる」を期待して書いたら flake したので書き直した。

### 7.2 発見・修正したもの（S1-S6）

- **S1. sync binding の欠落（根本原因）**: `sync.Map`/`Pool`/`Cond` の hostType と、`sync.Locker`（`KindInterface` typedef、`Lock`/`Unlock` のメソッド要件）、`NewCond`、`OnceFunc`/`OnceValue`/`OnceValues` を `intrinsics.go` に追加。`sync.Locker` への適合判定は `satisfiesIface` → `methodsOfValue` が GoValue の reflect メソッドセットを列挙するので `var l sync.Locker = &sync.Mutex{}` が型チェックを通る。スクリプト側の自作 Locker も `scriptLocker` アダプタ（`vc.Member` 経由で Lock/Unlock を引く）で `NewCond` に渡せる。
- **S2. GoValue 包みの func が呼べない**: `p.New()`（Pool.New は `func() any` のホスト func）や `m.Range` に渡したコールバック変数の呼び出しが `is not callable`。`v.call` に `*runtime.GoValue`（`reflect.Func` kind）ケースを追加し、arity/variadic/marshal 処理は selectMember と共有の `callReflectFunc` に抽出。
- **S3. host 型の keyed composite literal が拒否される**: `&sync.Pool{New: f}` が `cannot initialize host type with fields`。`makeComposite` の HostNew 分岐に `initHostLiteral` を追加 — ポインタを辿って struct を取り、`FieldByName`+`CanSet`+`toReflectValue`+`Set` で書き込む。**大きな副産物**: minigo は stdlib の import を本物の GOROOT ソースから解釈するため、`var blackHolePool = sync.Pool{New: ...}` を持つ `io` パッケージが丸ごとロード可能になった（S1+S3 の両方が必要）。`testdata/decltypes` は「io が解決不能」前提のケースだったので `io.NotAReader`（メンバ不在のまま）に差し替えて前提を保全。
- **S4. 別 goroutine からの `v.Call` が VM を破壊（本ラウンド最重要）**: `wg.Go`（Go 1.25）や他 goroutine からの `Pool.New` 起動など、「ホストが保持したスクリプト callable をホスト goroutine から呼ぶ」ケースで `v.frames` がデータ競合し crash。`v.Call` に goroutine-id による所有チェック（`callDepth`/`callGid` を `callMu` 下で管理、`goroutineID()` は `runtime.Stack` ヘッダの `goroutine N` を parse、失敗時は 0=foreign 扱い）を入れ、呼び出し中の VM に対する別 goroutine からの Call は `v.Spawn(callee, args)` + `t.Wait()` に回送して `t.Result` を返す。`runtime.Task` に `Result` フィールド追加（Done close より先にセットするので Wait 帰還後の観測が保証される）。これは §4 で「既知の限界」としていた **adaptFunc hazard をクラスごと解消**したもので、`time.AfterFunc` のような未 bind API も理論上は bind 可能になった。
- **S5. 埋め込み host 型のメソッド/フィールド昇格**: `type gated struct{ sync.Mutex }` の `g.Lock()` が `gated has no field or method Lock`（fuzz の新規発見）。`e.findMethod` に `emb.HostNew != nil` 分岐を追加し、`methodsOfValue(recv)` のメソッドセット＋`hostFieldName`（exported フィールド用）で検証した上で `(nil, recv, true)` を返す — interface 埋め込みと同じく、実解決は VM の `selectMember` の reflect 経路に任せる。`g.New` のような **promoted フィールドへの書き込み**は `setField` 側の `promotedHostField` が担当（埋め込み GoValue を発見して再帰 setField → `*runtime.GoValue` ケースの reflect Set）。読みは findMethod→selectMember 経路で通る。
- **S6. host ポインタの型 assert が false**: `c.L.(*sync.Mutex)` が失敗（`sync.Cond.L` は exported な `sync.Locker` フィールド）。`typeMatches` の `KindPointer` 分岐は `runtime.Deref` に頼るが GoValue は剥がせない。GoValue ケースを追加 — host typedef の box はポインタ零値（`sync.Mutex` → `*sync.Mutex`）なので `reflect.TypeOf(gv.V) == reflect.TypeOf(et.HostNew())` で一致判定、host typedef でない `*T` は型名スペル比較にフォールバック。

### 7.3 fuzz corpus（~/concfuzz の再構築）

前ラウンドの `~/concfuzz` は消えていたため、sync 中心に再構築（`cases/` 20 ディレクトリ、`run.sh` = `timeout 8 ./minigo-bin <dir> F` で `expected.txt` 照合、`ERR` はエラー期待）: `syncmap_ops` `syncmap_concurrent` `syncmap_range` `syncmap_vals`（closure/struct/slice/nil の格納忠実性）`syncpool` `syncpool_zero` `synccond` `synconce` `synconce_panic`（panicking Do で once 消費）`synclock` `syncwg` `syncwg_neg`（負カウンタ panic）`synclocker` `syncembed` `syncembed2` `syncmap_lit` `syncmisc`（TryRLock/TryLock + defer）`syncmulti` `syncmulti2` `syncneg`（`NewCond(42)` が trap）。

結果: 全 20 ケース通過、`-race` ビルドでも警告なし、3 回連続実行で flake なし。新規発見は S5（埋め込み昇格）のみ — S1-S4 はユニットテスト段階で潰せていた。

### 7.4 残件

- `time.AfterFunc` は依然未 bind（S4 で安全に bind 可能にはなった — 要追加実装）。
- `sync.Map` の複数戻り値メソッド（`Load`/`LoadOrStore`/`Swap`/`CompareAndDelete`/`LoadAndDelete`）は minigo の Tuple 規約どおり `v, _ := m.Load(k)` の 2 変数受けが必要 — minigo 全域の既存仕様。
- `var c sync.Cond`（零値 Cond: `c.L` が nil）は Go と同じく使用不可 — `NewCond` 必須。
- 複数の埋め込み host 型が同名の exported フィールドを持つ場合の ambiguity 判定は未実装（最初に見つかった方が勝つ — Go はコンパイルエラー）。

---

## 8. 計画外: sync-round leftovers 処理ラウンド（テスト先行 + language corpus 回帰）

§7.4 の残件2件（`time.AfterFunc` 未 bind、埋め込み host 型の ambiguous selector が先勝ち）を、テストを先に書いてから処理したラウンド。途中で埋め込み解決の構造そのものに手を入れる計画外の意思決定が2つ入った。

### 8.1 `time.AfterFunc` の bind

- `h.fn`/`h.fn1` 系のヘルパは第一引数 `vc runtime.VMCaller` を捨ててしまうため、生の `&runtime.BuiltinFunc{Fn: func(vc, args)}` で bind した（S4 が整えた窓口）。コールバックは `vc.Spawn(f, nil)` で起動する — Go の「コールバックは専用 goroutine で呼ばれる」意味論どおりで、タイマー goroutine は join せずに返る。
- **失敗経路が重要（レビュー指摘で修正）**: 初版は `vc.Call(f, nil)` + `panic(err)` だったが、reroute 経由では Spawn のラッパーが既に `p.fail(err)` しており、その後の `panic(err)` はタイマー goroutine 上の **未回復 panic** として host プロセスごと落とす競合になっていた（実クロックで exit 2 のクラッシュを確認）。`vc.Spawn` に変えたことで、コールバックの panic は通常の `go` goroutine panic と同じく `p.fail` → `proc.done` → root の procExit アンワインド経路で `Run` のエラーになる（`AfterFuncPanic` で「timer boom」が返ることを固定）。
- **タイマーは登録元プロセスと共に死ぬ（レビュー指摘で修正）**: `vc.Spawn` は呼び出し側 VM の proc が nil のとき detached な proc を新設するため、元の `Run` が返った後にタイマーが発火するとコールバックが別プロセスとして動き、後続の `Run` から `fired` への書き込みが観測できていた。Go ではプロセス終了で pending タイマーは死ぬ。`VM.procDead` フラグを追加し `ReleaseProc`（proc 死亡時）にセット、`Spawn` で「proc nil + dead」「生存 proc だが done 済み」の両経路で spawn を拒否して完了済み Task（procExit）を返す。bind 側は `vc.Spawn(f, nil)` のまま — proc 寿命の縛りは VM 層の責務に寄せた（`TestAfterFuncDiesWithProc` で Arm→sleep→Read が 0 を返すことを固定）。
- コールバック引数の事前検査は `adaptFunc` の受理集合（`*runtime.Function`/`*Closure`/`*BoundMethod`/`*BuiltinFunc`/`*Named`）に揃えるが、**nil 系（`runtime.Nil`/`*TypedNil`/`*IfaceNil`）は受理する（レビュー指摘で修正）** — `time.AfterFunc(time.Hour, nil).Stop()` は Go でも true を返す。nil 関数呼び出しは発火時に失敗させる: `call` ディスパッチで裸 `runtime.Nil` も nil-deref panic に含めたので、発火すれば通常の goroutine 失敗経路で `Run` に返る（`AfterFuncNilStop` / `TestAfterFuncNilFire` で固定）。
- synctest バブルでは `time.AfterFunc(time.Hour, f)` も即時発火するため決定的に書ける: `AfterFuncFires`（バッファ付き ch 経由で 7）、`AfterFuncStop`（`t.Stop()` 後は select の default 分岐で 9）、`AfterFuncPanic`（コールバック panic → Run が panic を返す）。実クロック側も 50ms で発火と、panic 時のクリーンなエラー返却を手動確認した。

### 8.2 埋め込み host 型の ambiguity — `promotedField` への統合（計画外の意思決定①）

対象バグは `struct{ sync.Mutex; sync.RWMutex }` の `t.Lock()` が先勝ちで `Mutex` を選ぶこと。実装を追うと host 埋め込みの解決は **first-wins の2系統に散っていた**: 読みは `findMethod` の `HostNew` 分岐、書きは `promotedHostField`。個別に ambiguity を足すと判定ルールが散在するため、**host 埋め込みを `promotedField` の BFS に統合する**判断をした — スクリプト埋め込みと同じ「浅い深さ優先・同深度ヒットは ambiguous trap・nil ポインタ経路は dereference panic」のルールにそのまま乗る。

- `promotedField` の戻り値を `(*runtime.Struct, int, runtime.Value, bool)` に拡張（3番目 = host レシーバ）。呼び出し側（`structMember`/`namedMember`/`setField`/`setLitField`）は `hrecv != nil` なら `selectMember`/`setField` に流すだけで、解決機構は1本に集約された。`promotedHostField` は削除。
- host 埋め込みはリーフ: `hostMemberInner(embTd.HostNew(), name, methods)` が boxed 型の promoted メンバーとその内部深さを返す。格納値が nil でも名前解決は成功し、その後 `hostNilEmbed` が nil ポインタ経路として `nilDepth`/`nilPaths` 会計に載せる（script 側 nil-ptr 埋め込みと同じ nil pointer panic になる）。
- **存在確認は型レベルで行う（レビュー指摘で修正）**: 初版は `hostField(zero, name)` — ゼロ値を dereference して `reflect.Value.FieldByName` を呼んでいたため、`struct{ *template.Template }` のように「host 型自身が nil の匿名ポインタフィールドを持つ」ケースで promoted フィールド（`s.Root`）の存在確認自体が `reflect: indirection through nil pointer` で panic した。`reflect.Type.FieldByName`（`PkgPath` で非公開を除外）に変更し、dereference は実際のメンバーアクセス側に委ねた（`HostSubEmbedField` = `s.Root != nil` で固定）。
- **host 型内部の昇格深さも BFS に乗せる（レビュー指摘で修正）**: 上の修正で `Template → *parse.Tree → Root` のような「host 型内部を1段潜った promoted メンバー」も検出されるようになったが、全てスロット直上の深度（depth+1）として数えていたため、`struct{ *template.Template; Local }` の `Local.Root`（浅い）と `parse.Tree.Root`（深い）が同深度衝突 → false ambiguous になった。`hostMemberInner` が内部深さを返す（フィールドは `sf.Index` 長、メソッドは埋め込み型を再帰する `hostMethodInner` — ポインタ経路の貢献メソッド集合も `underPtr` で追跡）し、BFS は `abs = depth+1+inner` の deferred ヒットとして保持: そのレベルに到達した時点で script ヒットと競合させ、walk が尽きて残った deferred は最浅のものが勝つ（`HostSubEmbedDepth` = 7 で固定）。nil-embed の nilPaths も同じ abs 計算を使うので、nil `*T` 経由の深いメンバーが浅い実メンバーと誤って同深度判定されることもない。
- `embedTypeDepth` にも host リーフ判定を追加（host 型の内部は minigo から不透明なので d=1 で打ち止め — nil `*sync.Pool` 埋め込み経由の「型レベルで存在確認」にも使われる）。
- script メソッドも `embTd.Methods[name]` で `methHits` として計数するよう拡張。メソッド自体は `findMethod` に委譲して戻り値にはしないが、同深度での衝突は数える: `{sync.Mutex; locker2}`（host メソッド vs script メソッド）も `{mA; mB}`（script メソッド同士）も ambiguous trap になる。mixed-kind（host メソッド vs script フィールド等）も同様。

**定義型はメソッドを継承しない（レビュー指摘で修正）**: `type MyMutex sync.Mutex` は Go ではメソッド集合が空 — `{MyMutex}` の `x.Lock()` はコンパイルエラー。peel 前の typedef を `raw` として保持し、メソッド判定は `raw.Methods` と `hostMemberExists(..., methods bool)` に「embed が host 型そのものか」を渡す形に分離した: 定義型経由では underlying の**フィールドのみ**昇格しメソッドは昇格しない（`type MyPool sync.Pool` の `x.New` は通り、`x.Lock` は `has no field or method` trap — Go と同じく拒否される）。script 側の `type B A` も同じ規則になる。

`findMethod` の HostNew 分岐は `promotedField` を通らない呼び出し経路のセーフティネットとして残した。

### 8.3 nil `*T` host 埋め込みの扱い（計画外の意思決定② + レビュー指摘で修正）

`struct{ *sync.Pool }` で格納値が nil のケース。Go はセレクタ自体を静的に解決するため「フィールドがない」ではなく dereference で panic する。`hostMemberExists` を型レベルで評価する設計にしたことで、このケースは script 側の nil-ptr 埋め込みと同じ nilPaths 機構に自然に乗る — 単独パスなら nil pointer panic、実パスと同深度なら ambiguous trap、という対称性が無料で得られた（`NilHostPtrEmbed` で固定）。

ただし一律 panic は誤りだった: **nil レシーバを許容するポインタレシーバのメソッドは Go では呼べる**（レシーバに nil がそのまま渡るだけ）。`hostNilCallable` を追加し、メンバー種別で分けた: フィールドと値レシーバのメソッド（`T.MethodByName` で見つかる = レシーバ評価が deref を要する）は従来どおり panic、ポインタレシーバのみのメソッド（`*T` にだけ存在）は nil を通して呼び出す（`NilMethod`=7、`NilValueMethod`/`NilField`=panic で固定）。

### 8.4 追加テスト

`testdata/concurrency/main.go` + `concurrency_test.go`:

- `TestConcurrencyBlocking` 表: `AfterFuncFires` `AfterFuncStop` `ShallowHostWins` `NamedHostFieldEmbed` `NamedScriptFieldEmbed`
- `TestAfterFuncPanic`: コールバックの panic が `go` panic と同じ proc 失敗経路で `Run` に届くこと（タイマー goroutine の未回復 panic で host が死なない — レビュー指摘の回帰）
- `TestAfterFuncDiesWithProc`: 終了済み run のタイマーが発火しないこと（`AfterFuncArm` で登録→実クロック 100ms 待機→`AfterFuncRead` が 0 — レビュー指摘の回帰。実クロックが要るので synctest 外）
- `TestAmbiguousSelector`: `AmbigHostMethod`（host+host メソッド）`AmbigHostField`（host+host フィールド書き込み）`AmbigMixedField`（host フィールド vs script フィールド）`AmbigMixedMethodField`（host メソッド vs script フィールド）`AmbigMixedMethodMethod`（host メソッド vs script メソッド）`AmbigScriptMethod`（script メソッド同士）— 全て "ambiguous selector" trap 期待
- `TestDefinedTypeMethodSet`: `NamedHostMethodEmbed`（`{MyMutex}` の `Lock`）`NamedScriptMethodEmbed`（`{bDefined}` の `M`）— 全て "has no field or method" trap 期待
- `TestAfterFuncNilFire` + `AfterFuncNilStop`（表）: nil コールバックは登録できる — Stop すれば成功、発火すれば nil-call panic が goroutine 失敗経路で `Run` に返る
- `TestNilHostPtrMember`: `probehost`（Bind した `*T` host 型）の nil `*T` 埋め込み — ポインタレシーバ `M()` は nil レシーバで 7、値レシーバ `V()` と フィールド `N` は nil pointer panic
- `HostSubEmbedField`（表）: `struct{ *template.Template }` の promoted `Root` — host 型のゼロ値が nil 匿名ポインタを持っていても型レベルで解決し、実値の `*common` はアクセス時に deref される
- `HostSubEmbedDepth`（表）: `{*template.Template; tplLocal{Root int}}` — host 型内部の昇格フィールド（Template→*Tree→Root、深さ2）より script の直接フィールド（深さ1）が勝つ
- `TestNilHostPtrEmbed`: nil `*sync.Pool` 埋め込みへの `New` 書き込みで nil pointer panic

### 8.5 検証

- `make format` / `make lint` / `make test` 全 green、`-race` で警告なし
- language corpus（podhmo/minigo-usecasefuzz `language/`）: **38 PASS / 4 PASS-REJECT / 0 DIFF / 0 TRAP / 0 ACCEPT** — 前回記録の 35 PASS + 3 TRAP-by-design から `lim-*` 系も全て PASS になっていた（本ラウンドの変更によるものではなく、leftovers ラウンドで既に解消済みだったことを確認）

### 8.6 残件

- `findMethod` は embeds を順に見る DFS first-wins のまま — `promotedField` が単一メソッドヒットを返さない限り深さ順序は DFS 依存であり、浅い promoted メソッドと深い promoted メソッドが競合した場合（`struct{X{A}; Y}` で A.M が深く Y.M が浅い等）に深い方を返し得る。ambiguity の trap は防げたが、深さ順序の完全な正しさには `findMethod` 側の BFS 化が必要。
- interface 埋め込みの method-req 衝突は未計数（`struct{io.Reader; A}` で A も Read を持つ等 — Go はコンパイルエラー、minigo は iface の動的 dispatch として動く）。
- nil `*T` 埋め込み経由の promoted メソッド呼び出し: host 側はポインタレシーバなら呼べるようになった（`hostNilCallable`）が、script メソッド（`findMethod` 経由）は nil recv の `structOf` に失敗して "no field or method" trap のまま — Go は method value 評価時に nil pointer panic（値レシーバ）または nil を渡す（ポインタレシーバ）。また `{*S{*T}}` nil のような「script の nil-ptr 埋め込みを更に潜った先の host ポインタレシーバメソッド」も `embedTypeDepth` が nil パスとしか数えられないため早期 panic になる。
- `vc.Call` 経由の retained コールバック（adaptFunc で MakeFunc 化された sync.Pool.New 等）は detached proc で依然実行される — AfterFunc は Spawn 経路なので今回の `procDead` ゲートで止まるが、Call は新しい proc を開く設計で変えていない。proc 寿命の厳密な縛りを host コールバック全般に広げるなら別途検討。
