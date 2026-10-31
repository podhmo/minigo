# 追試: gosimilarity 実験（#1022）と後続 PR（#1023, #1024）

対象: podhmo/minigo `f191f06`（#1024 マージ後の main）、Songmu/gosimilarity `298b2cd`（realworld の pin と同じ）、minigo-usecasefuzz `2e39dfe`。
元レポート: [experiment-gosimilarity.md](./experiment-gosimilarity.md)。アルゴリズム側の調査（mizchi/similarity との比較、元レポート主張の検証）は [similarity-algorithms.md](./similarity-algorithms.md) に分けた。

計測マシンは元レポートと異なるので、絶対時間ではなく**ネイティブ比の倍率**で比較する。

## TL;DR

- **主張の大筋は再現した**: 5ファイル入力（659関数）で minigo 上の出力はネイティブと完全一致（110ペア、text/JSON ともに。JSON は後述の unexported フィールド問題を除く）。gosimilarity を minigo 本体に当てたときの A 節の「層横断の実コピー」も現 main でそのまま残っている（`memberDeclOf`/`memberDecl` 100%, `deepTwinOperands`/`twinOperands` 100% など）。
- **倍率は約100倍ではなく約160倍**: realworld ハーネスの `oracle_s`（約1.3s）は `task.sh native` の rsync + `git apply` + `go build` を含む。素のネイティブ実行は 0.57s（GOMAXPROCS=1 でも 0.73s）、minigo は 87〜93s。元レポート内でも「約150倍」と「約100倍」が混在している。
- **#1024 の目玉（cells ブロック再利用）は実質空振り**: 変数をセレクタのベースにする（`x.f` / `x.M()`）、`=` 代入、`op=`、添字代入、range 代入先、のどれか1つでも持つチャンクは `chunkCellsEscape` が「エスケープする」と判定する。ホットパスの `distance.func1`（2145万回呼び出し）を含め、計測したほぼ全フレームが fresh 確保側に落ちていた。PR の -20% alloc はフレーム/スタック/argbuf プーリング分と読める。
- **ただし直しても伸びしろは小さい**: 判定を上限まで緩めた（不健全な）計測専用版で 86〜88s → 79.8s（約-9%）。「アロケーション削減で残り10〜30%」という元レポートの肌感とは整合する。
- **maphash ハングの真因は「解決できない linkname の zero-return shim」**: `maphash.MakeSeed` は `for { s = randUint64(); if s != 0 { break } }` で、`runtime_rand`（`//go:linkname runtime.rand` の本体なし宣言）が黙って 0 を返すため無限ループする。asm 経路ではない。同じ shim で `weak.Make` は黙って空の `Pointer` を返す。
- **本物の flagx は（スキャン用途なら）そのまま動く**: TODO の「weak が未バインドなので flagx は素では動かない」は誤り。`weak` は GOROOT ソース解釈で zero shim を通り、flagx は `weak.Pointer` を map キーにしか使わないので、FlagSet が1つなら全キーが衝突しても実害がない。必要なシムは maphash→fnv だけ。`--help` は別ギャップ（`reflect.Type.Size` 未実装）でトラップする。
- **既存 TODO に無い不具合を複数発見**: `json.Marshal` が unexported フィールドを出力する（private データが漏れる）、`os.IsPathSeparator` 未バインド、slog JSONHandler が `"level":0` を出す、#1024 の frame pool と脱出した `yield` の組み合わせでライブフレームが壊れる、`json.Encoder` 経路の不具合群（#1023）、など。詳細は下記。

## 1. 計測

| 項目 | 値 |
|---|---|
| ネイティブ（patched, 5ファイル, warm） | 0.57s wall / 0.98s user（GC 並列）, GOMAXPROCS=1 で 0.73s |
| minigo `f191f06`（CLI） | 88.9s, 86.4s, 92.8s（RSS 約265〜275MB） |
| realworld `run.sh gosimilarity-scan` | PASS, oracle_s 1.30 / minigo_s 92.90 |
| 倍率 | 約150〜165倍（wall）, 約175倍（CPU 時間） |
| `go build`（warm）単体 | 0.14s |

`--format json` も動く（#1023 で `json.NewEncoder` がバインドされたため）が、出力がネイティブと異なる（§3.1）。git 管理下のディレクトリを対象にした場合も `exec.CommandContext` 経由で完走し、ネイティブと一致した。

### プロファイル（自前ドライバ、in-process）

realworld の `PROFILE=1` は task.sh 型タスクを対象外にする（"profile skipped (task.sh drives a program, not one main)"）。#1024 の作者は別ドライバを使っていたと思われる。ここでは `gosimilarity.Run` を固定引数で呼ぶ main を足して `prof/main.go` で計測した。

- CPU: GC が約4割（`gcDrain` cum 41%, `tryDeferToSpanScan` flat 12%）。`VM.loop` cum 50%。
- alloc_space 合計 54.5GB（#1024 記載の 58GB とほぼ同じ）。内訳: `prepFrameSeen` 19.0GB（うち `cells = make(...)` 15GB, `fr.locals = make(...)` 3.9GB — どちらもエスケープ判定側）、`OpVar` の `&runtime.Cell{...}` 6.4GB、BuiltinFunc 経路の `defer func()` 2.9GB と `args = append([]runtime.Value{}, args...)` 3.0GB、`Walk.Container` 3.8GB。
- VM 計測（一時パッチでカウント）: `distance.func1` 2145万フレーム、builtin 呼び出しは `math.Min` 6379万、`len` 4917万、`make` 1088万。

## 2. 性能: 見逃されていたボトルネック

### 2.1 `chunkCellsEscape` がほぼ常に true

`chunkCellsEscape` は `OpLocalRef` を1つでも含むチャンクを「cells が漏れる」とみなす。ところが `OpLocalRef` は `&x` 以外でも出る:

- 代入先: `x = y`、`a, b = b, a`、`x op= y`、`s[i] = v` / `m[k] = v` のベース、range の代入先（`refTarget`）。
- セレクタのベース: `x.f` / `x.M()`（`selectorBase` → `refTargetBase`）。コンパイラは型を知らないため、ポインタレシーバ呼び出しに備えてフィールド読み出しでも常に変数のセル ref を積む。

小さなプローブで確認すると、`y++` だけの関数以外（`a = b` 単独でも）がエスケープ判定になる。gosimilarity では理由別に集計して `distance.func1` は `LocalRef slot=0`（引数 `a` の `a.label`）、`Scanner.next`、`FileSet.file` なども同様だった。

計測（同一マシン、交互実行）:

| 版 | wall |
|---|---|
| main `f191f06` | 88.9s, 86.4s |
| 代入先 ref を「一時的」とマーク（健全） | 87.3s, 86.3s |
| + セレクタベース ref も一時扱い（**不健全**、上限見積もり用）+ `OpVar` のセル上書き再利用 | 79.8s |

健全な修正の方向: 静的判定をやめ、プール由来の cells を楽観的に使い、**実際にセルが公開された時点**（`&x` の評価、ポインタレシーバへの束縛やメソッド値化、ローカル捕捉クロージャの生成）でフレームに「leaked」フラグを立て、`freeFrame` ではそのブロックを捨てる（`cellsShared` と同じ扱い）。`OpVar`/`OpRenewVar` も leaked でないフレームではセルを上書き再利用できる。期待値は上の上限どおり **1割弱**。

### 2.2 builtin 呼び出しの固定費

`len`/`make`/`math.Min` は専用 opcode を持たず、`VM.call` の `*runtime.BuiltinFunc` 分岐を通る。この分岐は `for {}` の中に `defer func(){...}()` を置いているため open-coded defer にならず、**呼び出しごとにヒープ確保**される（2.9GB）。さらに #1024 で引数スライスを毎回コピーするようになった（3.0GB。以前は `popArgs` が確保していたので差し引きゼロだが、OpCall 側の削減は builtin には効いていない）。

- defer を分岐ごとヘルパ関数に切り出せば open-coded になる（挙動不変）。
- 引数を保持する builtin（`task.F` 等）だけフラグでコピーし、他は argbuf をそのまま渡す。
- `len`/`cap` の専用 opcode（シャドウされていない場合のみ）。`len` は1回のスキャンで約5000万回呼ばれる。

### 2.3 倍率の天井について

元レポートの「JIT 無しのバイトコード VM として標準的〜やや良い」「10倍速には AOT/JIT が要る」という見立ては妥当。ただし前提の倍率が 100倍ではなく 160倍程度なので、「やや良い」は言い過ぎで「標準的」が近い。GC が4割を占めるので、上記 2.1/2.2 を合わせて alloc を半減できれば 15〜25% 程度は見込めるが、それ以上はディスパッチ・`coerceConcrete`・`selectMember` などの固定費の積み上げになる。

## 3. 正しさ: 見つかった不具合

### 3.1 `json.Marshal` / `Encoder` が unexported フィールドを出力（既存、TODO 未記載）

```go
type A struct {
	Name string `json:"name"`
	tree *node
	body string
	a, b int
}
json.Marshal(A{Name: "n", body: "secret", a: 1})
// go:     {"name":"n"}
// minigo: {"name":"n","tree":null,"body":"secret","a":1,"b":0}
```

gosimilarity の `--format json` で `"tree": null, "body": ""` が余計に出て発覚。private データを外部に出してしまう類の不具合なので優先度は高い。

### 3.2 zero-return shim（解決できない linkname / 本体なし宣言）

`//go:linkname local runtime.rand` のような宣言は、`Hooks.Linkname` で解決できないと黙ってゼロ値を返す（`Hooks.Linkname` のコメントどおりの仕様）。結果:

- `maphash.MakeSeed()` が無限ループ（0 を引き続ける）。
- `weak.Make(p).Value()` が nil を返す（`runtime_registerWeakPointer` が 0）。flagx は偶然動く。

「本体なし宣言の呼び出しはトラップ」が minigo の契約（黙って違う値を返さない）に沿う。どうしても必要な少数（`runtime.rand`、`weak` 一式など）は `asmImpl` 等でバインドする。

### 3.3 その他のギャップ（gosimilarity 経路で発見）

- `reflect.Type.Size` が `minireflect.RType` に無い（flagx の `--help` で `no member Size on host value *minireflect.RType`）。
- `os.IsPathSeparator` 未バインド（gosimilarity に存在しないパスを渡すとエラーメッセージではなくトラップ）。
- slog `JSONHandler`: `json.NewEncoder` バインドでトラップは消えたが、`"level":"INFO"` が `"level":0` になる（`slog.Level` の `MarshalJSON` が使われていない）。TODO の該当エントリは記述が古い。

### 3.4 #1024 / #1023 のコードレビュー（差分実行で確認）

- **#1024: 脱出した `yield` を range-over-func のループ終了後に呼ぶとライブフレームが壊れる**。go は `range function continued iteration after whole loop exit` で panic。#1024 以前もループ本体を黙って再実行していた（既に誤り）が、#1024 後は yield クロージャが掴んでいたフレームが pool 経由で別関数に渡っているため、回復不能な nil deref / index -1 になる。`driveFuncIter` が producer の正常終了時に `it.Exited` を立てていないのが原因で、立てれば go と同じ panic になる。
- **#1023 `json.Encoder.Encode`**（`Encode` が reflect 経由の host メソッドで、`json.Marshal` の raw-args 経路を通らないことに起因）:
  - 型付きコンテナ（`[]map[string]any`, `[][]any`, `map[string][]any`, `map[int]any` など）に script 値が入るとインタプリタ内部の構造を辿ってエラーになる。
  - `""` キーの orig スロット判定が実データと衝突: `Encode(map[string]any{"": &x, "a": 1})` が `5` になる（go `{"":5,"a":1}`）。
  - `Encode(rows)` 後に呼び出し側の `[]map[string]any` が `GoValue` に置き換わり `rows[0]["a"]` がトラップ（`callReflectFunc` の write-back。`text/template` の `Execute` でも以前から起きる既存バグ）。
  - `MarshalText` を持つ map キーが失われる。
  - 修正の方向: `Encode` を `json.Marshal` と同じ raw-args BuiltinFunc にする。
- 細部: cycle エラーに ` via <type>` が無い、`%T` が `*minigo.jsonEncoder` / `minigo.cleanupHandle` を表示する。
- 範囲外: `iter.Pull`/`iter.Pull2` に script の `iter.Seq` を渡すと壊れている（`next()` が即 false、`Pull2` は "next called again before yield"）。#1024 の前後で同じ。

#1024 について、GOROOT/test 約1150本の新旧差分実行と `-race` ビルドでは、上記以外の退行は見つからなかった。

## 4. 元レポート・周辺の記述で直すべき点

- 「ネイティブ ~1.4s, ~100倍」→ 素の実行は約0.6s、約160倍（`oracle_s` はビルド込み）。
- maphash ハングの原因推定（「asm フォールバック経路か」）→ zero-return shim による無限ループ。
- 「flagx は weak のため素では動かない」→ スキャンは動く。`--help` だけ `reflect.Type.Size` で落ちる。
- minigo-usecasefuzz `realworld/tasks/gosimilarity-scan/task.sh` のコメント（`AddCleanup` / `exec.CommandContext` 未バインド）と flagx シムの理由付けは #1023 以降古い。シムは maphash の1箇所に減らせ、`.git` 外に置く制約も不要。
- 「リテラル種別化」は不正確（値も label に入る）、型比較はフィールド順に敏感、など。アルゴリズム側の訂正は [similarity-algorithms.md](./similarity-algorithms.md) §4。

## 5. セレンディピティ（範囲外の気づき）

- **realworld の task.sh 型タスクはプロファイルを取れない**。gosimilarity のような CPU 負荷系こそ欲しいので、`task.sh profile` モード（Run 相当の main を生成）か、`minigo run` に `--cpuprofile` 相当を足すのが安い。
- **builtin 経由のコストは呼び出し回数に比例して効く**: このワークロードでは builtin 呼び出しが1億2000万回超あり、「1呼び出しあたり数十バイト」の固定費が無視できない。perf を詰めるなら関数呼び出しより先に builtin 経路を見る価値がある。
- **アロケーション量と wall は比例しない**: 計測専用版で alloc を2割強削っても wall は約-9%。GC は並列に走るので、alloc 削減の効果は wall より user 時間に出る。PR の効果報告は wall と CPU 時間の両方を載せるとよい。
- **リポジトリ内 `.claude/worktrees/` に 18 個の古い agent worktree が残っている**（`git worktree list`）。`grep -r` などリポジトリ全体を舐める操作に混ざる（gosimilarity は dot ディレクトリを無視するので今回は影響なし）。
- gosimilarity 自身の性能（ネイティブ）: CPU の約半分が `distance` のクロージャ、割当の98%が呼び出しごとの memo map と DP 行。インタプリタ上でも同じ形がそのまま増幅されるので、「スクリプト側のアルゴリズムが alloc-heavy だと VM の alloc も増える」二重効果がある。詳細は [similarity-algorithms.md](./similarity-algorithms.md) §5。

## 再現メモ

- 入力とシムは minigo-usecasefuzz の `realworld/tasks/gosimilarity-scan`（`input/` 5ファイル、`shim.patch`、`flagshim.go`）。
- maphash だけ fnv に差し替えた本物の flagx 版: `tree.go` の `maphash.Seed`/`MakeSeed`/`Hash` を `fnv.New64a()` に置換するだけ。
- プロファイル: `gosimilarity.Run(ctx, []string{"<abs path>/input"}, os.Stdout, os.Stderr)` を呼ぶ main を足し、realworld の `prof/main.go` を `-dir` に向けて実行。入力は絶対パスで渡す（エンジンの起点ディレクトリが main のディレクトリになるため）。
