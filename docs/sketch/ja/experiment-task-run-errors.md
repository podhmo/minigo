# task-run: 壊れた入力への応答 — 実験レポート

対象: `examples/task-run`（minigo 上で動く mage/go-task 風タスクランナー）
基準: `examples/convert-define` の README「[Reading a failure](../../examples/convert-define/README.md)」が掲げる理想 —

> エージェントに親切なツールとは、直接書く場合に得られていたフィードバック（どこが、なぜ、誰のせいで壊れたか）を、入力側の語彙で返し直してくれるツールである。そして、それができないときに、成功したふりをしないツールである。

この文を判定基準に分解すると次の 3 つになる。

1. **非ゼロ終了 + 内容**: 壊れたら exit != 0 で、メッセージが「何が」を名指す
2. **入力側の語彙**: メッセージがスクリプト/コマンドラインの書き手が分かる単位（関数名、行、コマンド文字列、引数の個数）で語る — `runtime.Nil` や内部型名ではなく
3. **成功したふりをしない**: 返せない結果を空やゼロ値で誤魔化さない。静かに何事もなかったことにしない

「壊れた」は日常的な開発で起きうる不整合を想定して分類した: スクリプト（Taskfile）の不備、スクリプトから呼ばれるコマンドの不備、そこへ渡される入力の不備、定義と呼び出し側のドリフト、そして環境（cwd・ファイル体系）との不整合。

## 1. 実験方法

`go build` した `task-run` バイナリに対し、`/tmp` 配下に置いた壊れた Taskfile を `-f` で指定して実行し、exit code と stderr/stdout を記録した。再現ケースは本ドキュメント末尾「付録」に一覧する。

## 2. 良い挙動（理想に合っている部分）

先に、すでに理想を満たしているところを確認しておく。

| ケース | 実測 | 評価 |
|---|---|---|
| 存在しないタスク名 | `task NoSuchTask: no task "NoSuchTask" (run with -l to list)` exit=1 | 行動の指針（`-l`）まで返す |
| 非公開関数を指定 | `task helper: no task "helper" (run with -l to list)` | 同上 |
| タスクでないシグネチャ | `task Calc: Calc is not a task (params must all be string, result empty or error)` | 理由を言語化している |
| 引数個数不一致 | `task Greet: takes 1 args (name), got 0` / `task NeedsTwo: takes 2 args (a, b), got 1` | パラメータ名まで出る |
| タスクが error を返す | `task Failing: task body says no` exit=1 | どのタスクが・何を返したか |
| 複数タスクの途中失敗 | `Ok1 Bad Ok2` → ok1 の出力の後 `task Bad: bad task` で停止、Ok2 は走らない | make 的な fail-fast。誰が死んだか明確 |
| 未定義識別子（リネームし忘れ） | `task Default: runtime trap: undefined: renamedHelper` + トレースバック `File "...", line 6, in Default()` と該当行テキスト | 直接 `go run` したときの compile error に相当する位置情報が返る |
| スクリプト panic / 型エラー | `panic: boom from task` / `runtime trap: unsupported types: int64 + bool`、いずれも `File "file", line N, in Fn()` + 行テキスト | どこが・なぜ、が十分 |
| init 時 panic | `main.__init__()` フレームまで出る `File "...", line 3, in main.__init__()` | パッケージ初期化中の死も追える |
| dep の失敗 | `task Default: runtime trap: dep Bad: dep exploded`（SerialDeps も同様） / panic なら `dep Bad: panic: dep panicked` | dep 名を含むエラー連鎖 |
| F 引数個数違い | `dep Emit: not enough arguments to Emit: 0 given, want 1` / `too many arguments to Emit: 2 given, want 1` | 呼ばれる側の名前で語る |
| F 引数型違い | `dep Emit: runtime trap: cannot use int as string` + 両側の行番号 | 同上 |
| Deps に関数以外 | `task.Deps: cannot use string as a dependency` / `cannot use int64 as a dependency` | 渡した型を名指す |
| 自己・2 項の循環（直系） | `dep B: dep A: task.Deps: dependency cycle at B` | 検出して止まる（ただし §3-G1 の穴あり） |
| 存在しない import | `runtime trap: import nosuch/pkg: resolve "nosuch/pkg": ... could not be resolved` + `__init__` の該当行 | パスは入力語彙。三重表現は冗長 |
| シェルエラーの passthrough | `task.Sh("echo to-stderr >&2; exit 2")` → `to-stderr` が stderr に流れ `exit status 2` | 子プロセスの言葉は届く |
| 存在しないコマンド (Run) | `task Missing: exec: "nosuchcmd-xyz": executable file not found in $PATH` | コマンド名あり |
| `task.RunIn` の存在しない dir | `task RunInMissing: chdir /abs/no-such-dir: no such file or directory` | 解決後パスを名指す |
| `task.Output` の失敗 | `(v="", err=exit status 1)` — 2 値契約を維持 | 契約破壊なし |
| 存在しないコマンド (Sh) | `sh: 1: no-such-cmd-xyz: not found` が stderr に流れつつ `task MissingSh: exit status 127` | shell の言葉は届くがラッパ側は G2（スタック適用後は `sh -c "...": exit status 127`） |
| 存在しないコマンド (Output) | `err=exec: "no-such-cmd-xyz": executable file not found in $PATH` | Run と同じ exec エラーが 2 値目に返る |
| 成功時のストリーム分離 | `task.Sh("echo out; echo err >&2")` → `out` は stdout、`err` は stderr（`2>/dev/null` / `1>/dev/null` で分離検証） | チャネルが正しく分かれる |
| 失敗時も途中出力を捨てない | `echo partial-out; echo partial-err >&2; exit 3` → partial-out→stdout、partial-err→stderr、その後 `exit status 3` | 失敗しても子の出力は流れ残る |
| dep 連鎖の失敗 | `SerialDeps(A→B→C)` で C が失敗 → `task TopChain: runtime trap: dep A: ... dep B: ... dep C: exit status 1` + 各階層ごとの Traceback ブロック | どの経路で落ちたか連鎖で読める |
| `task.Target` の dep 欠損 | `ok=false err=stat /abs/no-such-dep.txt: no such file or directory` | パスを名指す（§3 の注意書きあり） |
| 別 cwd からの実行 | Taskfile ディレクトリ基準で動く（設計通り） | — |
| 再帰呼出 | `runtime trap: stack exhausted: frame limit 10000` で死ぬ（プロセスは死なない） | loud failure ではある（§3-G7 参照） |
| flag エラー | `flag provided but not defined: -x` + Usage、exit=2 | Go flag の標準挙動 |

## 3. ギャップ（理想から外れているところ）

### G1. 並列ブランチを跨ぐ依存サイクルが検出されず、サイレントにデッドロックする — 最悪の失敗形

最小再現:

```go
func Default() { task.Deps(A, B) }
func A() { task.Deps(B) }
func B() { task.Deps(A) }
```

`task-run Default` は **何も出力せず永久にハングする**（timeout 8s で強制 kill した）。失敗するどころか診断がゼロで、今回の観測で唯一「結果が返ってこない」系の壊れ方だった。

原因は検出方式にある。cycle 検出は claim 時に「待つ相手（dep を所有するタスク）が自分の spawn 祖先にいるか」だけを見る（`taskInAncestry`）。直系の連鎖（`task-run A` で A→B→A）では、B の goroutine 上で A が再 claim されるため祖先チェックが効いて `dependency cycle at B` と出る。しかし A と B が**ルートの `Deps(A, B)` で兄弟として claim される**と、spawn 親はどちらもルートであり、互いは祖先に現れない。wait グラフ上のサイクル（T_A waits T_B, T_B waits T_A）は spawn 木を見ても見えず、デッドロックが成立する。

対処: claim 時の祖先チェックに加えて、**wait 時**に「自分が待とうとしている dep のタスクが、自分へ戻る wait 辺を持たないか」を検査する必要がある（wait グラフのサイクル検出）。Runner 側で実現可能（`depState`/`runtime.Task` は Runner が全部握っている）。

### G2. `task.Sh`/`task.Run` のエラーが「どのコマンドか」を捨てる

```go
func False() error { return task.Sh("false") }
```

```
task False: exit status 1
```

`exec.ExitError` が素で返るため、メッセージには exit status しか残らない。タスク名（`task False:`）は出るが、**その中で死んだコマンド文字列がどこにもない**。`task.Sh("nosuchcmd-xyz")` はシェル自身の `sh: 1: nosuchcmd-xyz: not found` が stderr に流れるので事実は残るが、返り値の error はやはり `exit status 127` のみ。スクリプトが `fmt.Println(err)` したり複数の Sh を連続で呼んだりすると切り分けが難しい。

期待する形（入力側の語彙）: `task.Sh("false"): exit status 1` のように、書いたコマンド列を含めて返す。`task.Run`/`task.RunIn`/`task.Output` も同じ。

### G3. `string` と宣言された引数への非文字列が黙って文字列化される

`strArg`/`strArgs` は `strOf`（fmt.Sprint 相当）で何でも文字列にする:

| 呼出し | 実測 |
|---|---|
| `task.Sh(42)` | `sh: 1: 42: not found` → `exit status 127` |
| `task.Sh(true)` | `sh -c "true"` → **exit=0 で静かに成功** |
| `task.Run(42)` | `exec: "42": executable file not found in $PATH` |
| `task.Run("echo", 1, true, "x")` | `1 true x`（動く） |

スタブの宣言は `task.Sh(cmd string)` / `task.Run(name string, args ...string)` なので、直接書いた Go コードでは `task.Sh(42)` はコンパイルエラー（`cannot use 42 as string`）。インタプリタ上ではそれが「`42` というコマンドが PATH に無い」という、**責任の所在を環境に押し付ける誤った診断**になる。`task.Sh(true)` は成功のふりの最たるもの（シェルの `true` が走るだけ）。数値を引数に渡すケース（`task.Run("echo", 1)`）は動くので、厳格化するなら「name/dir/cmd 位置は string 必須、args はスカラーのみ許容」のような段階も選択肢 — だが少なくともプログラム名位置の非文字列は常にバグなので弾くべき。

同じ穴が `task.Log` にもある（`...any` なので設計としては正しいが、`task.Log(fmt.Println)` は `&{fmt.Println 0x80a2c0 ...}` と内部構造をダンプする）。

### G4. ロード失敗が `task <name>:` と帰属される

```
task Default: parse /tmp/taskrun-exp/cases/missing.go: open ...: no such file or directory
```

ファイルの読み込み（open/parse）に失敗したときでも、呼んだタスク名が prefix される。読者には「Default タスクが壊れた」ように見えるが、壊れているのは Taskfile か `-f` 指定。`-l` 側は `parse <file>: ...` と prefix なしで出るので一貫していない。convert-define はここを `Fix the command-line arguments`（`-f` の責任）と `Fix the define file`（ファイルの責任）で分けている。

### G5. 同じパッケージの兄弟ファイルを読まない

`testdata` ではなく実運用で自然にやりたくなる形:

```
dir/
  Taskfile.go   # func Default() { Helper() }
  helpers.go    # package main; func Helper() {}
```

`task-run -f dir/Taskfile.go Default` → `runtime trap: undefined: Helper`（行番号つき）。

Go の語彙では同じ `package main` 配下のファイルはスコープを共有するので、`go run ./dir` なら動く。task-run は `-f` の単一ファイルだけをロードする（`engine.LoadFile`）ので、兄弟ファイルの宣言は丸ごと見えない。エラー自体は正直に出るが、`undefined` という内部解決の失敗として返るため、ユーザーは「import が要る?」「名前が違う?」と迷う。`-l` にも兄弟ファイルのタスクは列挙されない（`helpers.go` 側にだけ定義したタスクは実行も列挙もできない）。

対処案: `-f` で指定されたディレクトリの同パッケージ `.go` ファイルを併せて読む、あるいは `undefined: X` を出す際に「`-f` は単一ファイルだけを読む」旨のヒントを添える。

### G6. 関数名の再宣言が無言で後勝ちになる

```go
func Default() { task.Log("first") }
func Default() { task.Log("second") }
```

`go build` では `Default redeclared` のコンパイルエラー。task-run では `-l` には 1 つだけ並び、実行すると **無言で後者が走る**（`second`）。定義側の壊れ（コピペ事故、merge の残骸）を「成功したふり」で通す。index が name → decl の map なのでエンジン側で重複検出しないと見えない。

### G7. スタック枯渇のトレースバックが読めない量になる

再帰（`func f() { f() }`）は `runtime trap: stack exhausted: frame limit 10000` と同一 `File "...", line 3, in f()` の羅列を吐く。エンジンには既に上限があり、`renderFrames` の `maxTracebackEntries = 1000` で先頭500+`... 9000 frames elided ...`+末尾500（実測 ~2000 行）まで絞られる。ただしこれは件数の cap であり、同一フレームの連続反復は畳まれない — cap 後でも ~2000 行の同一行が残り、人間もエージェントも読めない。長い run を `先頭 ~50 フレーム + ... repeated N more times ...` 程度に畳むのが親切（CPython の `RecursionError` がやっている形を、デバッグに必要な実フレーム数を残して控えめにした形。既存の head/tail cap とは補完関係で、fold を cap より先に走らせると同一再帰は ~50 行になる）。

### G8. その他・小さいもの

- `task.Deps(nil)` → `task.Deps: cannot use runtime.Nil as a dependency` — `runtime.Nil` は内部語彙（`nil` と言ってほしい）。
- `task-run Default -l` → 非 flag 引数の後の `-l` はタスク名扱いで `no task "-l"`（Go flag の標準動作だが、利用者はハマりがち）。
- `task.Sh("")` → `sh -c ""` が exit 0 で静かに成功。空コマンドはたいていスクリプト側の組み立てミスだが、警告は出ない。
- `task.Deps()`（引数なし）→ no-op で成功。同様にスクリプトバグの可能性を無言で通す。
- `task.Deps(fmt.Println)` → builtin 関数も dep として受理され、何事もなく「成功」する（意味のない dep を止めない）。
- `task-run Failing Greet:x` のような複数指定で先頭が失敗した場合、残りは走らない — それ自体は正しいが「スキップされた」ことは明示されない。
- コマンドがハングした場合にタイムアウトはない（`task.Sh("sleep 60")` はそのまま寝る）。make/mage も同じなので許容と考えるが、G1 のデッドロックとは別の「返ってこない」形として記録。
- `-l` はロードを parse+index のみで済ませるため、init 時に panic するファイル（`var x = boom()`）や import が解決不能なファイルでも `Default()` を正常に列挙する。「一覧」という質問には正直だが、「このファイルは走れるか」に対する成功のふりになりうる — 少なくともドキュメントで言及する価値がある。
- `-f` にディレクトリや空文字列を渡すと `parse <dir>: read <dir>: is a directory`。分かるが、`is a directory` は OS の語彙で、convert-define 的に言えば `taskfile <path> is a directory. Fix the command-line arguments` になりうる。

## 4. 評価: 理想との距離

3 つの基準に戻すと:

1. **非ゼロ終了 + 名指し** — 概ね合格。ほぼすべての失敗が exit=1 と、タスク名/dep 名/位置つきのメッセージを返す。唯一 exit が返らないのが G1（永久ハング）。
2. **入力側の語彙** — 半分合格。トレースバックは file:line + 行テキストで十分育っている一方、コマンド失敗（G2: `exit status N` のみ）、型違反の黙約（G3）、ロード失敗の帰属（G4）、内部語彙の漏れ（`runtime.Nil`, `strOf` による fmt ダンプ）で「直接書いたときのフィードバック」が失われる。
3. **成功したふりをしない** — ほぼ合格だが例外が二つある: `task.Sh(true)` のような意味のない入力の黙殺（G3）、関数名再宣言の後勝ち（G6）。`-l` の lazy さは仕様として擁護可能だが要ドキュメント化。

「誰のせいで」の帰属という観点では、convert-define が `Fix the command-line arguments` / `Fix the define file` / `This is a convert-define generator bug` の三段を先頭行に置いているのに対し、task-run の現状は「タスク名 / dep 名 / 内部 trap」でほぼ一貫してスクリプト側を指す。`parse <file>:` がタスク名 prefix を被る G4 はこの帰属が滲む箇所。壊れている層（CLI 引数 → Taskfile → スクリプト内呼出し → 子コマンド → 環境）ごとに帰属を分ける接尾辞を入れると、テーブル化できる:

| 壊れた層 | メッセージ先頭の帰属例 |
|---|---|
| コマンドライン引数 | `... Fix the command-line arguments` |
| Taskfile のロード | `... Fix the Taskfile` |
| タスク名/引数 | `task X: ... Fix the invocation` |
| スクリプト実行中 | `task X: ...`（現状通り、トレースバック併記） |
| 子コマンド | `task X: <cmdline>: exit status N`（G2 修正） |
| runner 自身 | `... This is a task-run bug`（現状は到達不能のはず） |

## 5. 改善提案（優先度順）

1. **G1 デッドロック → 検出可能なサイクルエラーに**。wait 時に wait グラフを辿る検出を `deps`/`serialDeps` に追加する。Runner 内だけで完結する修正。対処がないと「返ってこない」ので最優先。
2. **G2 コマンドエラーにコマンド列を添える**。`task.Sh`/`task.Run`/`task.RunIn`/`task.Output` が返す error を `task.Sh: "false": exit status 1` 型に wrap する。
3. **G3 `string` 引数の厳格化**。少なくともプログラム名・dir・cmd 位置は string 必須（`task.Sh(42)` を `task.Sh: arg 1 must be a string, got int64` に）。`strOf` の適用範囲を絞る。
4. **G4 ロード失敗の帰属分離**。`task <name>:` prefix をタスク起因のエラーだけに限定し、ロード失敗は `-l` と同じくファイル主体で返す（型付きエラーで区別）。
5. **G6 同名タスクの再宣言を検出**。エンジンの index が上書きするなら runner/エンジン側で重複宣言をエラー化する（Go の `redeclared` に相当）。エンジン側の変更が必要なので次のラウンドでも可。
6. **G5 兄弟ファイル**。`-f` 指定ディレクトリの同パッケージ読み込み or `undefined` 時のヒント。同様にエンジン寄り。
7. **G7 トレースバックの反復畳み**。エンジン側、同一 `File ... line N, in f()` 連続を畳く。
8. 帰属接尾辞の整備（§4 の表）。ドキュメントと合わせて。

## 6. 付録: 再現ケース一覧

実験に使った Taskfile 断片（すべて `package main`、必要に応じて `import "task"`）:

- CLI 層: `-f` に非存在/ディレクトリ/空文字、未知 flag、タスク名なし、`Task:`、`:foo`、引数過不足、`Default -l` の順
- スクリプト層: 構文エラー、未定義識別子、`panic`、型エラー、`error` 返却、init 時 panic、import 欠落、兄弟ファイル参照、関数再宣言、再帰
- dep 層: 直系サイクル、兄弟 claim 経由サイクル（デッドロック再現）、dep の error/panic、`task.F` の引数違い・型違い、非関数 dep、nil dep、builtin dep、self dep、serial/parallel 混在
- コマンド層: `task.Sh` の `false`/未存在 cmd/exit 2/空文字/非文字列、`task.Run` の未存在/権限/非文字列、`task.RunIn` の未存在 dir/絶対パス、`task.Output` の失敗・未存在 cmd、stderr passthrough、stdout/stderr の分離（成功時・途中失敗時）、dep 連鎖（`SerialDeps(A→B→C)`）のエラーラベル、ハング
- 環境・不整合層: `task.Target` の dep 欠損・target 欠損・dir dep、別 cwd からの呼出し、`-l` が init 失敗を無視すること、生成物（`app.out`）がディレクトリ化している場合など

観測日: 2026-10-04。`task-run` の実装は `runner.go`（claimDep/depState/ ancestry チェック、`strOf` による緩い marshalling、`errOf(cmd.Run())` による素の ExitError 返却）と `main.go`（`task %s:` による一律 prefix）に由来する。
