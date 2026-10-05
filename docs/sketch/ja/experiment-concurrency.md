# 真の並行並列 — minigo へのホスト goroutine 並行処理導入の実験レポート

対象: `podhmo/minigo`（スタックマシン VM を持つ Go サブセットインタプリタ）
目的: 従来の「シングルスレッド近似」（`go` が同期的・チャネルが無制限キュー・select がソース順 probing・ブロック操作は trap）を、**本物の並行並列**に置き換える。検証は `testing/synctest` を軸にテストを書きながら進めた。
ブランチ: `devin/<ts>-concurrency`（PR 参照）

---

## 1. アプローチの選択

並行処理の実現方法として大きく2案あった:

- **グリーンスレッド型**: 1つの VM が複数の論理スレッドを実行し、命令境界でスケジューリングする。VM 側で完全に制御できるが、スケジューラ・プリエンプション・ブロック検出を自前で実装する必要がある。そして `time.Sleep` や `sync.WaitGroup` 等の「真のブロック」を再現するにはホスト側との橋渡しが複雑になる。
- **ホスト goroutine 型**: `go` 文ごとに新しい `*VM` をホスト goroutine で実行し、チャネルは本物の `chan runtime.Value` を共有する。

**ホスト goroutine 型を採用した**。理由は次の通り:

1. Go のセマンティクス（ブロッキング・ランダムな select・バッファ溢れ）が `reflect.Select` 一発で再現できる。自分で書いたスケジューラの再現精度を気にする必要がない。
2. **`testing/synctest` がそのまま効く**。synctest の判定対象は「永続的ブロッキング操作」（チャネル操作・`time.Sleep`・`sync.WaitGroup.Wait`・`Mutex.Lock`）であり、これらがホストの本物である限り、バブル内でブロッキングの決定性・フェイククロック・デッドロック検出が全部タダで手に入る。グリーンスレッド実装だと synctest の条件を自力で満たす必要があった。
3. 1 VM = 1 goroutine に限定すれば、VM 内部の状態（フレーム・スタック・inflight）は依然としてロック不要。共有されるのはパッケージレベルの状態だけで、スコープが明確に切れる。

## 2. アーキテクチャ

```
Engine ── e.vmm (root VM) ── Call()
                              │ proc{done, fatal}  ← 1 run = 1 process
                              ├── OpGo → Spawn(fn,args) → &VM{H, proc, task} on host goroutine
                              │      ├── spawned VM has own frames/stack
                              │      └── shares: package Globals, channels, matCache
                              └── proc.done closes at root Call return
                                  → parked channel ops abort with procExit
```

- **`proc`**: 1回の run に属する全 goroutine の終了点。`done chan` と `fatal error`（最初の致命的エラー）を持つ。`v.Call` が外側境界で proc を生成し、戻り時に `done` を close（`EnsureProc`/`ReleaseProc` で builtin 等の中間呼び出しの入れ子にも対応）。
- **`runtime.Task`**: `go` の spawn ハンドル。`Done`・`Err`・`Parent`（spawn ツリー）・`Aborted`。`VMCaller` に `Spawn`/`Task` を追加し、ホスト側（task-run）も同じ機構で並列化できるようにした。
- **`runtime.Chan`**: `{Elems []Value; Closed bool}`（キュー近似）→ `{C chan Value; Typ *TypeDef}`（本物のホストチャネル）。`make(chan T, n)` のバッファが効く。
- **`procExit`**: プロセス終了時にパーク中の op を解除するための専用 unwind 型。`*runtime.Panic` ではないので `recover()` には見えず、defer は走り、spawn 境界で飲み込まれる（Go で他の goroutine が「プロセス終了」で消えるのと同じ）。`asScriptPanic`/`unwind`/`runDefers`/`asError` の各通路で透過させた。

**goroutine の panic**: 子 goroutine の非 procExit エラーは `p.fail(err)` でプロセス全体を落とし（Go の panic がプロセスを殺すのと同じ）、root の `Call` は自身の結果（procExit 含む）より `p.fatal` を優先して返す。結果: `go func(){ panic("x") }()` は `Run` のエラーとして「x」が返る。

## 3. 実装の要点

### チャネル — `chan runtime.Value` への全面移行

- `OpSend`/`OpRecv`/`OpRecvOK`: `chanOf` で runtime 値を `reflect.Value`（ホストチャネル）に正規化し、`reflect.Select` でブロッキング送受信。**全てのブロッキング op は proc.done も select に含める**——プロセスが死んだ時、どこかの goroutine が nil チャネルにでもパークしていれば解放して unwind する。
- `chanOf` は `*Chan`/`Cell`/`Named`/`TypedNil`/`IfaceNil`/`GoValue` を吸収: nil チャネルは「永遠にレディにならない nil channel」として扱い（Go 通りデッドロックする）、`time.After` 等が返す `GoValue{chan T}` も素通りで select できる。
- `close` はホストチャネルを `close` する。二度目の close / close 済みへの send はホスト側の panic がそのままスクリプト panic になる（Go 通り）。
- `range over chan` は `Iterator{ChRV, ETyp}` に持ち替え、閉じるまで実際にブロック受信する。
- `len`/`cap` は `len(ch.C)`/`cap(ch.C)` に。

### select — `reflect.Select` + ジャンプテーブル

`OpSelSend`/`OpSelRecv`（probing）を `OpSelArm`/`OpSelWait` に置き換え:

```
operand eval (source order, into $selN temps)   ← Go 規格通り
per case: OpLocal chan(+val) + OpSelArm          ← *runtime.SelArm を push
OpSelWait ncases hasDefault                      ← reflect.Select
  jump table: OpJump ×ncases (+default)          ← dispatch は ip 書き換え
case bodies (unchanged, bindRecv で受信値を束縛)
```

- `OpSelect` という名前はメンバー選択に取られていたため `OpSelArm`/`OpSelWait` に分けた。
- `reflect.Select` が ready な case をランダムに選ぶ（Go 通り。`TestSelectRandomPick` で両側が選ばれることを確認済み）。
- default ありでも `done` は select に含める——死んだプロセスの goroutine が default ループで busy-spin するのを防ぐ。

### lazy init を「呼び出した側の VM」で実行

`p.Member` の `EnsureReady` は `e.vmm` 上で `__init__` を動かしていた。spawned goroutine から初めてパッケージに触れた時、root VM 上で init を走らせると goroutine 同士が同じ VM を共有して壊れる。

- `Member(name, mat)` → **`MemberV(name, mat, run func(*Function) error)`**。VM 側は `v.memberOf` で「この VM で init を走らせる runner」を渡す。
- ホスト側（`Engine.Call`/`EvalExpr`/dispatch の型解決など）は `p.RunInit`（`newVM` を1回作って呼ぶ runner）が既定になり、どの goroutine からでも安全。
- `initOnce` 中の panic は「once 消費済みで半初期化状態」にならないよう、`runBootstrap` の defer で error に変換してから re-panic させる（次の caller には `initErr` が返る）。

### 共有状態のスレッドセーフ化

spawned goroutine が並列に解決する可能性のある箇所を洗い出して対策:

- `Env`: RWMutex（パッケージ変数・globals の読み書き）
- `Package.State`: `atomic.Int32` + `State()`/`SetState()`（書き込み ~10 箇所を更新）
- `Engine.buildMu`: `buildPackage`/`SourceOf`/`LoadFile` の全体を直列化（同じパッケージの並行 cold-load が1ビルドに収束）
- `p.indexed chan struct{}`: `indexFiles` 完了で close。`MemberV` はこれを待つ——未インデックス化中に触った goroutine が中途の index を読まない
- `MatCache`: `matMu` + dedup map（同時 materialize が2つの `*TypeDef` identity を生まない。`x.(T)` の assert が identity 依存のため）— **build 中はロックを外す**（build が別 decl を解決しうるため再入デッドロックになる。これが実際に見つかったバグ #1）
- `Engine.mu` 既存領域（`pkgs`/`byDir`/`binds`/`srcs`）は per-access lock のまま、`specials` 読み出しも同じ mutex 配下へ

### `sync` パッケージと `TypeDef.HostNew`

`var wg sync.WaitGroup` が `GoValue{*sync.WaitGroup}` になる仕組み:

- `hostType(name, new)` = `&TypeDef{Kind: KindStruct, HostNew: func() any}`。`runtime.Zero`/`makeComposite` が `HostNew` を先に見て `&GoValue{td.HostNew()}` を返す。
- `selectMember` の `Cell`/`FieldRef`/`IndexRef` 経路に `*runtime.GoValue` の再帰を追加（`var mu sync.Mutex` が cell に入っている時、セルの中身のホストメソッドに到達できるようにした）。
- `sync.WaitGroup`/`Mutex`/`RWMutex`/`Once`、`time.After`/`NewTimer`/`NewTicker`、`time.Nanosecond`〜`Hour`、`runtime.NumGoroutine`→実値+`Gosched` を bind。`Once.Do` はスクリプト func を host func に marshal できない関係で未サポート（許容範囲）。

### task-run — `task.Deps` の真の並列化

`ran`/`running` マップを `depStates map[string]*depState` に置き換え、モード横断の claim/dedup/cycle 検出を実装:

- `claimDep(key, cur, label, spawn)`: key ごとに最初の一発だけ実行権を得る（二番目の waiter は spawn せず結果を待つ）
- 並列 `Deps`: claim → `v.Spawn(wrap, nil)`（`resultErr` で error 戻り値を call error に変換 → プロセスが死ぬ fail-fast）
- 直列 `SerialDeps`: claim → 自前で `v.Call`、結果を `st.done`/`st.err` に書く。`waiter()` は `st.task ?? st.owner` を返し、**祖先チェック `taskInAncestry` で循環を検出**（serial 経路では dep 自身の task がないので、claimant の task で辿る）
- 並列 dep の `fmt.Fprintln(stdout)` 等が同時に走るため、runner の streams を `syncWriter`（1 行分の Write を mutex で直列化）に包んだ

## 4. テスト・検証で見つかった実害バグ

### `MatCache` の再入デッドロック

`MemberV` が `p.MatCache(d, e.materialize)` を呼び、`e.materialize` 自身も `p.MatCache(d, e.materializeOne)` を呼ぶと、非再入 `sync.Mutex` で hang。テストが 60 秒黙ったままだった（SIGQUIT で `Package.MatCache` の `sync.Mutex.Lock` が積み上がっているのを確認）。

→ 構造として「ロック中に build を呼ぶ」ことが根本的に危険なので、**double-check 方式**（外で build → lock して勝者のみ書く）に変えた。敗者の値は捨てるが、以降の caller 全員が最初の identity に収束する。

### `depState.wait()` のデータレース（-race で検出）

並列 waiter 同士が `st.err = err` を書き合っていた。task.Wait() が一度しか書かない時点で waiter 側の上書きは不要だった。

### テストデータが近似セマンティクスに依存していた

`testdata/deferchan` は「send が絶対にブロックしない」前提で `make(chan int)`（unbuffered）に順次 send していた。本物のブロッキング導入で hang/デッドロックに変わった関数を修正（`make(chan T, n)` + `close`、channel-synced な `GoSync` 書き直し等）。同様に `decltypes/ChanElemTyped`、`intrins/RuntimeGoroutines`（実値に変更）も対応した。

## 5. synctest での検証

`concurrency_test.go` は全て `synctest.Test(t, ...)` バブル内で動く:

- **ブロッキングの決定性**: unbuffered send/recv・`select` のパーク・`WaitGroup`/`Mutex`・`range over chan` が全て goroutine ハンドシェークで進行する
- **フェイククロック**: `time.After(time.Hour)` と `time.Sleep` 中の goroutine が即座に resolve する
- **goroutine panic 伝播**: `PanicInGoroutine`/`NilChanBlocksThenPanic`/`EmptySelectThenPanic` がハングせずエラーで返る
- **detached リーク検出**: `DetachedLeak`（メイン終了後に残る parked goroutine）はバブルが「goroutine が残っている＝終了していない」と判断して検証できる

## 6. 残っている近似

- **ルートが永久にブロックした場合**: Go ならデッドロック panic になるが、minigo は「プリエンプト不可」なので本当にハングする（`test.timeout` まで）。デッドロック検出は今回スコープ外。
- **スクリプトレベルの共有**: `*runtime.Cell`/`Map.Pairs`/`Slice.Elems` は並列アクセスでデータレースになりうる。`sync.Mutex`/`chan` を通るのが正しい使い方で、Go 同様 UB として扱う（task-run の `fmt.Fprintln` は Runner が直列化した）。
- **`runtime.GOMAXPROCS`** は読み取り専用のまま（インタプリタの並列性はホストの GOMAXPROCS に従う）。

## 7. task-run の follow-up

`plan-task-runner.md` §7 で「far-future」とされていた parallel deps が本物になった。残りの課題:

- task arg typing（`task-run Build:dbg` スタイルのフラグ/環境変数バインド）
- discovery（`-f` 以外の上行探索）
- namespaces（`task:sub` スタイルの grouping）

## 8. まとめ

ホスト goroutine 型を採用したことで、(a) `reflect.Select` がブロッキング・ランダム選択・バッファを1発で再現し、(b) `testing/synctest` がそのまま使えて実験サイクルが高速だった、という2点が設計上の大きな利点になった。`proc.done` でプロセス境界を明示したことで「goroutine panic が全体を殺す」や「main 終了時に parked goroutine を解放する」という Go の振る舞いもセンチネル `procExit` 一発で表現できた。`MemberV` の runner 注入と `MatCache` の double-check は goroutine-per-VM 構造に特有の落とし穴だった。
