# TODO.md 未実装項目の優先度整理

2026-11-17 時点 (main 6dfa72f) のスナップショット。TODO.md の open 項目 (`[ ]` と `[-]` の子) を優先度で分けた索引。
項目の本文・最新状態は TODO.md 側が正であり、ここでは取り組み順を決めるための分類だけを持つ。

## 優先度の基準

- **高**: 今すぐやりたい・必要なもの
- **中**: 必要なもの・役に立つことがあるもの
- **低**: 今のところ役に立つとは思わないが、理論的には確実な前進であるもの

分類で意識したこと:

- minigo の契約は「成功したふりをしない」。無言の誤答・ハング・実害のある書き込み喪失は高寄り
- 逆方向の差分 (gc はコンパイルエラーにする入力を minigo が受理する) は実害が小さいので低寄り
- `unverified` / 未計測 / 条件付き ("実ユースケースが出たら") / 到達不能の項目は低寄り
- 表示 (`%T`・要素順) だけの差分は低寄り
- 性能系は「明らかに遅いものを native 化する」ものだけ中寄り、それ以外 (interpreter 内部 tuning) は低。[experiment-oapi-codegen-perf](../experiment-oapi-codegen-perf.md) で "running work natively was the only lever worth more than ~7%"、[experiment-frame-path-perf](../experiment-frame-path-perf.md) でも landed 済みの cache 系を除くと残りの profile は flat (単一点 ~5% 以下) で、内部 tuning には天井があることが分かっている

## 高

### 契約違反: 無言の誤答・ハング

- **bodiless decls with an unresolvable `//go:linkname` silently return zero values** — `hash/maphash` の無限ループと `weak` の空ポインタの直接の原因。shim を trap 化し、実コードが必要とする葉 (`runtime.rand`、`weak.Make` など) を bind する。契約そのものの問題なので最優先
  - 付随: **`hash/maphash` hangs under interpretation**、**`weak` silently returns empty pointers** (上の shim 修正 + bind で解消)
- **`append` growth shares struct elements with the old backing array** — `b := append(a, ...)` 後の `b[0].X = v` が `a[0]` まで書き換える。平凡なコードで静かに壊れる
- **a panic inside a script `Write`/`Read` called from a host io helper becomes an error** — go では panic なのに `io.Copy` が `err="panic: boom"` を返して続行する。`callOrPanic` と同様に re-panic するだけで済む形

### host embed のメソッド promote (実コードで頻出の形)

- **a struct embedding a host struct is not an io.Writer for the io helpers** — `type T struct{ bytes.Buffer }` を `io.Copy(&t, r)` に渡すと trap。Go では embed から `Write` が promote される
- **a script struct embedding a host pointer does not satisfy interfaces through promoted methods** — `type F struct{ *os.File }` が `interface{ Name() string }` を満たさない。`testdata/difffuzz/multipart_fileheader_open` の PENDING ピンを直接塞いでいる

### エラー処理の定番パターン

- **`os.PathError`, `os.LinkError`, `os.SyscallError`, `os.NewSyscallError`, `os.Link` are unbound** + **`errors.As` into an `*os.PathError` target misses** — `errors.As(err, &pe)` が書けない・取れない。os エラー系はまとめて 1 ラウンドで潰せるはず (関連: `os.IsNotExist` on a typed-nil `*fs.PathError`)

### 現在の主用途 (storybook / preview)

- **Story preview: pick up live reload in consumers' copied helpers** (#1079) — 機構は landed 済み。go-classic-mpa-sample など consumer 側のコピーを同期して `-watch` を渡すだけ

## 中

### 正しさの差分 — go と結果が違う・妥当なコードが動かない

- **`iter.Pull` / `iter.Pull2` are broken for every seq** — `next()` が `0 false`、2 回目で panic。loud だが機能ごと死んでいる (source-interpreted `iter.Pull` のコルーチン切替)
- **a function-local struct type with an array field sized by a function-local const has a zero-length field** — go で動くコードが `index out of range` panic
- **compile: map-literal keys read as field names under a type parameter** — `M{k: 1}` が `"kk"` ではなく `"k"` を格納する。静かな誤答だが発火条件が generics 限定なのでここ
- **`errors.As(err, &ute)` with `*json.UnsupportedTypeError` is false** / **`errors.As(err, &me)` with `me *json.MarshalerError` fails** — Marshal まわりでよく踏む errors.As が取れない
- **a comparison's result is typed `bool` in the operand gate** — `w := v && x > 1` (v が `type B bool`) が trap。妥当なコードを弾く
- **generic inference through a `~*E` core type** — `zero(&x)` が constraint 違反で trap
- **Generic pointer method expression traps `cannot use &G as *G[T]`** — method expression の receiver check が uninstantiated `*G[T]` と比較している
- **explicit instantiation of a bound `slices`/`maps` function traps** — `slices.Contains[[]int]` が `cmp` コールバックとして渡せない
- **`reflect.ValueOf(fn).Call` on a MakeFunc'd variadic func passes a nil tail for zero extras**
- **`unsafe.Sizeof(f())` evaluates its operand and types the result `int`** — 被演算子の副作用が走る + `uintptr` であるべき

### host 境界の書き込み喪失・identity 欠落 (host-twin family)

- **a read of an unnamed host slice is a script copy, so writes through the alias are lost** — `h["A"][0] = v` は届くが `s := h["A"]; s[0] = v` や `sort.Strings(h["A"])` がコピーを書き換える。実アプリの header 操作で踏みやすい形
- **`bytes.Buffer.Bytes()` result does not alias the buffer** — 同じ copy 起因
- **host in-place writes to script map args are lost** — bound host func が受け取った script map への書き込みが呼び出し側に返らない
- **`&r.Form` of a host struct field is not a host pointer** — `json.Unmarshal(data, &r.Form)` が `Unmarshal(nil)` で失敗
- **host pointer deref, host field writes and host slice element writes** — `*f.Name` の deref trap、`ce.Fun = ...` の `cannot use *runtime.Struct as ast.Expr`、script-built node が host field に入らない
- **conversion of a boxed host named map to another map type traps** — `map[string][]string(r.URL.Query())` 等
- **`&s[i]` with an out-of-range index does not panic at the address-of** — 後の deref まで bounds check が遅延している
- **other nil host values still read as untyped nil** (`[-]` 親の open 部分) — named host func/chan type の nil が `== nil` になってしまう
- **`context.Cause` loses a typed-nil cause** / **`os.IsNotExist` on a typed-nil `*fs.PathError` does not panic**
- **general source-twin → host conversion at the host boundary** (`[-]` 親の open 部分) — `any`/`error` 型の境界が `*struct{...}` projection のまま、snapshot で alias でもない
- **array keys with interface elements** — `map[[1]any]int` の lookup が 1 を返す (go: 0)。静かな誤答だが発火形が狭い

### interface 判定・型名の解決

- **a host value satisfies an interface assertion by method name alone** — host reflect method に decl signature がないので `Unwrap() []error` と `Unwrap() error` を区別できない。`ifaceSatisfied` で reflect method type と interface member signature を比較する方向
- **`inspect.MethodSet` drops members promoted from host-bound embeds it cannot read** — `Implementers` の false negative

### 具体的な誤答 (個別関数・個別挙動)

- **`strconv.FormatFloat(1, 'f', -1, 64)` prints 0** — `a[0].(float64)` assert が untyped int const を 0 にする
- **math/big `Float` from source prints 0**
- **`json.Marshal` of a `float32` and of a `[]byte` diverge** — `0.1` → `0.10000000149011612`、`[]byte` が base64 にならない
- **ptr-receiver `MarshalText`/`MarshalJSON` on fields of an addressable parent is not seen** — gc は `json.Marshal(&v)` でフィールドの ptr-receiver marshaler を見る
- **`reflect.Value.Convert(string → []byte)` builds a different `[]byte` than `[]byte(s)`** — `reflect.DeepEqual` が false になる。helper の一本化
- **`Engine.fsPath` cleaning changes os semantics** — `os.Stat("")` が成功する、`filepath.WalkDir("lnk/")` が潜らない等
- **`*os.File.Name()` and `os.MkdirTemp`/`CreateTemp` results spell the absolute host path** — script 相対パスではなく abs が見える (エラーメッセージにも波及)
- **maps seq helpers over host maps stay eager and untyped** — host `map[any]any` に `maps.Keys` が snapshot、`maps.All`/`Insert` が reject
- **`Engine.Call`: named host maps/structs stay boxed as args** (`[-]` 親の open 部分) — named map arg で `m[k]` が `index on *runtime.GoValue` trap
- **FuncMap param/panic conversion gaps** — 関数ローカル型 param、`[]Level`/`map[Level]T` の untagged 要素、panic payload の scriptData projection、`got []interface {}` の綴り
- **`--src html/template` traps in `Template.Parse`** — bound text/template の内部を読んで `v.Name()` で trap

### 未 bind メンバーの補充 (小粒・束ねて進められる)

- **missing stdlib bindings: `sort.SearchInts`, `time.UnixMilli`, `unicode.Han`**
- **`os.FileMode` is undefined** — `fs.FileMode` の alias を io/fs 側の typedef に解決する必要
- **`os.IsPathSeparator` is unbound** — gosimilarity が `canonicalPath` で踏む
- **`syscall` errno and signal members are unbound** — `Errno` 型・`SIGUSR1` 等
- **unbound host members found in the #962 follow-up** — `net.ParseMAC`、`net.IPMask`、`time.LoadLocation`、`time.Tick`
- **`slices` binding is missing exported funcs** — `CompactFunc`/`Compare`/`CompareFunc`/`ContainsFunc`/`IsSortedFunc`/`MaxFunc`/`MinFunc`/`Replace`
- **`reflect.Type.Size` is missing on `minireflect.RType`** — flagx の usage printer が踏む (gosimilarity --help)
- **io helper edge cases** — nil `*bytes.Buffer` への `io.Copy` が panic しない、`MyBuf(b)` 変換 trap、`json.NewDecoder`/`Indent`/`Compact`、`hex`/`base64` encoder、`bufio.NewReadWriter` が未 bind
- **`--src` 系**: `--src errors` traps `cannot use interface value as EmptyInterface` in `abi.TypeOf` — source-interpreted `errors.As`/`Is` の経路

### 性能: 明らかに遅い部分の native 化・host 境界

「native で実行する」のみが ~7% を超える lever だった (experiment-oapi-codegen-perf) ので、性能で中に残すのはこの系だけ。

- **close the `os.ReadFile` gap to plain Go** — 32MB で ~560MB RSS。compact `[]byte` 表現か `string(...)` 直行 fast path
- **residual per-byte costs at the host boundary** — `[]any` 経由の `[]byte` 引数、Read 後の全バッファ copy-back、`namedSized`/`sizedIntTyp` の per-call コスト
- **per-call cost of host methods and interpreted stream packages** — bound method ~0.8µs/call、`bufio.Scanner` 320k 行 ~420ms、解釈実行の `compress/gzip` ~2s/320KB (native bind が lever)
- **perf: ~90µs per 1-byte script `Read` via `io.Copy` / `io.ReadAll`** — proxy Read ごとの全バッファ往復

### 基盤・ツール (採用済みの方向)

- **`syntax.ParseImports` / `syntax.ParsePackageClause`** → **`modulewalk` package** → **`ImportManager` port** → **`astwalk` extraction (consumer-first)** — go-scan 援用群。modulewalk が REPL introspection の供給元
- **`TypeExpr` resolution-path recording** — cycle/失敗時に経路を報告できるように
- **REPL-side package introspection** (user wish, #397) — repo-enumeration surface 上の meta-command
- **script profiler follow-ups** — per-function call counts (gosimilarity で必要だった)、alloc profile、closure の名前、host sleep の wall 計上、`TestScriptProfiler` の flaky 緩和
- **field↔comment correspondence surface** (#741) — param/result `*ast.Field` に乗らない comment の対応付け
- **CLI parity leftovers** — `minigo run a.go b.go` が `b.go` を Func 名と誤読 (multi-file `go run` 相当)
- **`minigo vet` hint: `os.ReadFile` used only as text** — vet 初の perf hint、agent 向け
- **Classify callback-bearing host fields before extending process-bound entry** (#1062) — process-bound caller 拡張の前段分類
- **Selective replacement of source functions for preview runners** — storybook で data 取得を fake するための engine/session-scoped 設計タスク
- **residual: `Node` views rooted at non-func decls** — const initializer や type-spec 内部への `Node` view root
- **reflect-facade leftovers** — `net/url` の default を source interpretation にするか判断、`internal/*` stub layer

### メンテナンス・検証の仕組み

- **run oapi-codegen-examples periodically** — #662/#675 で実際に壊れた実績のある回帰カナリア (~5min の定期実行)
- **oapi-codegen needs `--src` for seven packages** — `text/template` の script func 呼び出し + `hex`/`base64`/`base32`/`slices`/`maps` の不足メンバ
- **Use-case-fuzz leftovers** — 境界変更ラウンドごとに corpus 再実行
- **difffuzz harness / Intrinsic coverage + element types** — 継続タスク (bind 追加で coverage を広げる線)
- **dedupe the cross-layer copies gosimilarity found** (R1–R6, -452 行 verified) → **CI ratchet for new duplicates** → **split `vm/vm.go` and `intrinsics.go`** — この順で小 PR にする

## 低

### 表示系 (`%T`・描画・並び順だけの差分)

- **a seq handed to a host `any` spells `func(func(T) bool)` unless its elements are basic**
- **`%T` / `reflect.TypeOf` of an unnamed host slice misspell it** (`[]fs.DirEntry` → `interface{}` 等)
- **`%T` of a host slice of package types drops the package** (`[]*Ident` vs `[]*ast.Ident`、`%T` of `os.Stdout`)
- **`var sf bufio.SplitFunc = bufio.ScanLines; fmt.Printf("%T", sf)` prints the unnamed signature**
- **`fmt.Errorf` `%w` misuse prints `%!v`** — go は `%!w(<nil>)` 等
- **`max`/`min` of a time.Duration var and an untyped constant print the raw number**
- **fmt key order of a mixed-type `map[any]`** / **fmt orders composite map keys by their rendered text** / **NaN-keyed map entries print `NaN:<nil>`** — fmtsort の kind-first / element-wise 比較
- **`%T` of a recovered `runtime.plainError` prints `runtime.PlainError`** — host Go 型名が漏れている
- **`io.LimitReader(r, 2).(*io.LimitedReader).R.(*rd)` is false** — フィールド読み戻しで proxy が見える

### 「gc はコンパイルエラー・minigo は受理」方向の差分

誤ったコードを実行してしまうが結果は正しく、実害は typo の見逃し程度。

- **`==`/`switch` operand check: residual surface** (#724) — interface method 戻り値が不透明なので `f.Tag == s` が評価される
- **an unknown member of a bound package in a type position is silently accepted** — `var x *json.Nope` が通る
- **conversions between incompatible pointer types are not rejected statically** — `TL(&x)` 等が run-time まで行く/受理される
- **typed constants are not type-checked against host parameters** — `strconv.Itoa(c)` (c は `int8`) が通るが結果は正しい
- **`slices.Index([]uint8{}, 300)` runs instead of failing to compile**
- **a spread after extra variadic args runs instead of failing to compile**
- **a multi-value call is accepted as one argument among others** — `f("x", g())` が通る

### 定数ドメイン・スカラーの細かい差分

- **non-constant shift: non-modular ops between two untyped operands settle at int**
- **typed float constant expressions don't round to their type at each step** — `float64(float32(1)/3)` の精度差
- **`int(c)` / `float64(c)` / `string(c)` / `bool(c)` / `complex128(c)` drop the type** — `int(5)/2.0` → 2.5 (go: 2)。fix は `sync.Map` キーとの干渉がある
- **`max`/`min` ignore a NaN that is not the first operand**
- **`atomic.AddInt32((*int32)(&cv), 2)` retypes a named int32** — 変換後ポインタ経由の store がタグを落とす
- **`reflect.DeepEqual` on a host scalar named type against its conversion** — `time.Second` vs `time.Duration(1e9)`
- **`reflect` on a bound host scalar constant misses its host methods** — `NumMethod()` が 0
- **`append` growth capacity diverges for elements not 16 bytes wide** — `cap()` の値が gc とずれる

### map キー・ordering の edge case

- **uint64 above MaxInt64 in the ordering helpers**
- **`slices.Max`/`Min` with NaN and signed zero**
- **`json.Marshal(map[any]int{...})` should fail** — gc は unsupported type エラー
- **`map[any]` keys of named chan and converted struct types collide**

### 未検証・未計測・到達不能

- **unverified: `slices.SortFunc`/`SortStableFunc` order ties differently from go**
- **unverified: `vet.go` stub-call detection uses a per-decl shadow set** / **unverified: cost of the func-wide "possibly local" model**
- **`scriptError.Is`/`As` vs `Timeout` disagree on named-bool results** (suspected, unverified — difffuzz probe で確認してから)
- **`--src` template execution hot-path profiling** — 未計測。measure first
- **host maps and arrays lose element identity like slices did** — 到達する値がまだない

### inspect 残件 (under-resolution は設計上許容の方向)

- **other path-tail readers of an import's name** — `vet` が `yaml` 経由の stub call を見逃す (unverified 部分あり)
- **residual: composite-literal keys of an imported or type-parameter literal type stay unresolved**
- **unverified: dot-imported names**
- **duplicate local-name collection: `inspect.localNames` vs `vet.go`'s `declaredNames`** — 共有化 or `syntax.LocalScopes` 移行で消える

### REPL completion 残件

experiment でまだ merge するか未決の親 (`[-]`) の子なので、親が採用されてから。

- call-expression bases (`f().`、`m[k].` 等) / string-context completion (map keys) / untyped `var x = f()` in foreign packages / same-depth promoted-member ambiguity / promoted-method `Detail` の描画 / `m[k.` の suffix fallback / builtins の `Detail` 欠落

### 条件付き・実需待ち

- **`Engine.Call`: opt-in write-through for container args** — "実 write-back ユースケースが出たら" の条件付き
- **Story preview: generate display data from types** — "hand-written fixture が苦になってから" の条件付き

### 性能: interpreter 内部 tuning (realworld `grafana-openapi` 実測ベース)

重い script を ~3min の痛み閾値から遠ざけるためのものだが、内部 tuning には天井がある (上の基準参照) ので低に置く。再開するときのため大きい順に概ね残す:

- **frame allocation** (`prepFrame`, ~700MB/34%) — frame の pool 化または arena
- **`chunkCellsEscape` is true for nearly every chunk** — optimistic pool + "leaked" flag で ~-9% wall (計測済み upper bound)
- **interface satisfaction walks the dynamic method set twice** (`methodWalkU`, ~359MB/17%) — 1 walk で両 view
- **operand stack grows from empty on every call** (`frame.push`, ~244MB) — compile-time max depth で一括確保
- **`runtime.Tag` allocates a fresh `*Named` per tagging** (~208MB) — 同 tag 再 tag の box 再利用
- **non-param locals are allocated twice** — `prepFrame` 事前充填 + `OpNewLocal` の二重化
- **selector resolution re-searches by name** — `Def`+name → path の cache (dynamic re-dispatch は残す)
- **global resolution cost per `OpGlobal`** — `fileOf`+mutex `Env.Get` の inline cache (generation counter で invalidate)
- **every local is a heap `*Cell`** — capture/address-taken を compile 時に分類して plain slot 化
- **#485's selector lowering costs ~0.2 s** — var base の `refTargetBase` を pointer-receiver call に限定
- **`append` builds a whole zero value to learn the element type** — `VMCaller.ElemTypeOf(td)` で直接 typedef 解決
- **generic calls still pay per-call key spelling and type re-specialization** — call-site inline cache
- **gosimilarity residuals** — `prepFrameSeen` ~19GB、`arity` の `[]any` ~1.9GB、`CanonicalKey` ~3GB
- **`Map.Delete` is O(n)** — tombstone index (synthetic のみ)
- **perf: arithmetic on named-int values is slower than on plain ints** — `binaryOpFlags` の `Named{int64}` fast path
- **range-over-func pays a Go call per element; no compiler lowering** — script 記述 `iter.Seq` が `VMCaller.Call`/要素 (~2x)

### 設計変更が大きい・既知の壁

- **nested `UnmarshalJSON` receives the re-encoded literal, not the original byte span** — decoder-wide の設計変更 (span を持つ DOM)
- **scriptData fills fields before its eager methods run** — host call 側に lazy な形状が存在しない (documented approximation)
- **`uintptr`→`unsafe.Pointer` and `internal/reflectlite` reinterpretation** — `--src sort` / `--src strings` を塞ぐ壁
- **`encoding/gob` traps at init**
- **later / larger (perf)** — shared contiguous value stack、tagged `Value`、`orderSpecs` heap

### その他の小さなもの・記録のみ

- **Correct extract-schema plugin flag help** — 文言修正のみ
- `minigo help -h` — CLI parity 項目の子 (topic として読んでしまう)
- **leftovers from the #962 follow-up review** (record only) — dot import `regexp`、2 つの `os.Open` error の DeepEqual、zero `regexp.Regexp` の `String()`、`[]byte(ip)`

## 高を消化する順序の目安

1. linkname shim → trap (契約そのもの + 2 件の付随項目が解消)
2. host embed promote (io.Writer + interface 判定。PENDING ピンの unblock)
3. `append` の構造体要素共有 (小さい修正、広い影響)
4. script io panic → error (修正形が既に分かっている)
5. os エラー family の bind + `errors.As` (束ねて 1 ラウンド)
6. storybook live reload の consumer 展開 (minigo 側というより consumer 側の作業)
