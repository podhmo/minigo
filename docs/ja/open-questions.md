# Open questions（未決事項）

作業セッションだけでは決められなかった判断を置く場所。1 項目 1 問とし、何がブロックされているか、選択肢、誰が決めるかを書く。

## newsession-bind: 親で明示 `Bind` した stdlib path は `NewSession` に引き継ぐか？

session は独自の lazy table を構築する。session 側で pending のまま残っている path は親側の copy を skip するため、`e.Bind("os", custom)` のあとに `e.NewSession()` しても、session には custom ではなく default の `os` table が入る。

これは lazy install 以前からある挙動だった。eager install では session が構築時に stdlib を自分で bind していたため、やはり親による後からの re-Bind は届かなかった。ただし `NewSession` の doc には inherited host bindings が "resolve identically" とあり、stdlib path への custom re-Bind も含むように読める。

直すには registry が explicit bind と default bind を区別し、session 側で親の custom table を優先する必要がある。（[#1150](https://github.com/podhmo/minigo/pull/1150) の Devin Review からの escalate）

## M4-4: `runtime.HostNamedIs` / `runtime.HostTwinOf` は除去できる twin helper か？（解決済み）

2026-11-19 にユーザー判断で解決。両方とも reflect.Type↔typedef の残余 fallback として残す。除去のための二段階 capability 注入は、得られるものに対して複雑すぎると判断した。

除去自体は docs/next/ja/TODO.md の別タスク（"intern 表を拡張して HostNamedIs/HostTwinOf を除去する"）として追跡し、intern table が un-interned / stamp-less typedef もカバーできるようになったら再検討する。

## src-netip: bound-mode-only の stdlib package は `--src` で動かせるか？

`net/netip` を `ModeSource` で読むと package init で trap する。`unique.Make(addrDetail{})`（netip.go:69）が `unique.Make[T]` を呼び、これは `T` が非公開型 `addrDetail` の generic builtin である。内部では compiler intrinsic の `abi.TypeFor` と `unsafe.Pointer(&a)` による reinterpretation に落ちる。

これらは bind も source interpretation もできない。intrinsic には実行できる本体がなく、unsafe aliasing は interpreter の value model に載らない。

選択肢:

- interpreter 側に `unique.Make` / `abi.TypeFor` 用の intrinsic を用意する（asmimpl に似た、package ごとの escape hatch。ただし generics 用）
- `net/netip` は policy として bound-only と宣言し、`--src` は非対応と明記する（bound path は既に動く）
- source mode に一般的な `unsafe.Pointer` モデルを入れる。ただし pointer reinterpret cast の interpreter model が必要になり、かなり大きい

判断: podhmo に確認が必要。`--src net/netip` は `unique.Make(addrDetail{})` で再現する。bound `netip` は引き続き parity path。

## readfile-mem: どの slice model で os.ReadFile のメモリ差を埋めるか？

script の `[]byte` は 1 要素ごとに 16 バイトの `runtime.Value` を持つため、32 MB の `os.ReadFile` は RSS が約 560 MB まで膨らむ（issue #950）。候補は 2 つあり、どちらにも実コストがある。

- host の `[]byte` を backing にした compact `[]byte`。script が書き込んだときだけ要素 cell を materialize する。index / len / cap / append / copy / range / spread に対応するため、多数の `.Elems` 参照箇所に lazy element ops が必要
- 結果を `*runtime.GoValue` に box し、`string(b)` に fast path を入れる。ただし boxing だけでは `b...`、`copy`、`[]byte(b)`、`slices.*` が壊れ、個別の special case が増え続ける

判断: podhmo に確認が必要。局所的な修正ではなく memory model のトレードオフ。

PR [#1438](https://github.com/podhmo/minigo/pull/1438) は compact 案を段階導入する方向を推している。まず read-only compact byte slice を用意し、write 時に materialize し、その後に append / copy / write を native 化する段階案。ただしこれは推奨であって決定ではない。`.Elems` 直接参照は test を除く `.go` ファイルで約 313 箇所あるため、アクセサへの機械的置換コストもトレードオフに含める。

## exit-codes: panic と trap の process contract をどうするか？

`cmd/minigo` は現在、script panic と runtime trap のどちらでも exit 1。compiled Go binary は未回収 panic で exit 2 になる。一方 `go run` は child status を表示して自分自身は exit 1 になるため、difffuzz は exit code 比較に compiled oracle binary を使っている。CLI の usage error は既に exit 2。

選択肢:

- すべての run failure を exit 1 のままにする（現状維持）
- script panic だけ compiled Go binary に合わせて exit 2 にする
- panic は exit 2、minigo runtime trap は exit 3 に分け、CI で script のバグと interpreter の未対応面を区別できるようにする

判断: podhmo に確認が必要。`docs/sketch/ja/improvement-ideas.md` の U4 がこの判断待ち。

## check-command: deep trap 列挙は `minigo check` にするか `vet --deep` にするか？

`minigo vet` は既に未登録 stub call と performance hint を出すが、関数本体を eager compile まではしない。deep checker なら、対象 package だけを strict lazy compilation で compile し、埋め込まれた `OpTrap` の位置を `file:line:col` として列挙できる。import 先までは eager load しない。価値自体はあると考えているが、コマンド形と public contract が未決定。

選択肢:

- 独立した `minigo check` コマンドを追加し、`--json` も出す
- 静的解析の入口を 1 つに保ち、`minigo vet --deep` にする
- gopls/editor に任せ、vet は minigo 固有の host boundary の間違いに絞る

判断: podhmo に確認が必要。`docs/sketch/ja/improvement-ideas.md` の F1 が小さな CLI 追加になるか、新しい public command になるかが変わる。

## test-command: minigo は test runner を持つべきか？最低限の contract は何か？

`tools/tmpltests` は仕組みの一部を実証済み。stdlib test を scratch GOROOT にコピーし、`*_test.go` を rename し、generated driver と小さな `testing` shim で実行している。user-facing な `minigo test` は、この仕組みを supported command にするという意味。ただし互換範囲は product decision。

選択肢:

- 最小 runner を提供する。internal test files、`TestXxx(*testing.T)`、`Errorf/Fatalf/Run/Helper/Cleanup/TempDir/Skip`、`go test -v` 風出力。benchmark / fuzz / `TestMain` は非対応と明示する
- test execution は内部の conformance harness に留め、public command は追加しない
- いきなり CLI 互換を決めず、experiment command または example から始める

判断: podhmo に確認が必要。`docs/sketch/ja/improvement-ideas.md` の F2 がこの判断待ち。

## perf-gate: どの性能契約を CI で強制するか？

repository には benchmark があるが、継続的な performance gate はない。PR #1438 の案は、小さな代表 benchmark suite を `benchstat` で比較し、起動時間だけは product pitch の中心なので hard gate にするもの。ただし CI コストと noise 許容値は未決定。

選択肢:

- 起動時間だけを gate し、他の benchmark は informational にする
- 小さな benchmark set を CI で実行し、閾値を超えた悪化を PR にコメントするが fail はしない
- CI performance check は入れず、個別 optimization PR に測定を添える運用を続ける

判断: podhmo に確認が必要。`docs/sketch/ja/improvement-ideas.md` の P5 がこの判断待ち。

## typed-call-api: `CallAs[T]` の変換 contract をどうするか？

`Engine.Call` は `runtime.Unwrap(r)` を返すので、単純な scalar result は既に受け取りやすい。generic な `CallAs[T]` helper を作るなら、script composite、named host value、typed nil をどう deep conversion するかの contract が必要。そこを決めないと、簡単なケースを隠すだけで難しいケースでは結局失敗する helper になる。

選択肢:

- scalar / simple conversion だけに限定し、制限を明記する
- reflection ベースで任意の host type へ deep conversion する
- 実際の embedding use case が出るまで helper を追加しない

判断: podhmo に確認が必要。`docs/sketch/ja/improvement-ideas.md` の F5 がこの判断待ち。
