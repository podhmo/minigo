# スパイク: リアーキテクチャ計画の前提を検証する

ステータス: スパイク。main `a3f4e24`（2026-11-17）で計測した。インストルメンテーションはすべて使い捨てで、この変更には含めていない。再実行できるように、この文書に手順と数値を残す。

問い: リアーキテクチャ計画（[plan-rearchitecture.md](https://github.com/podhmo/minigo/blob/claude/vigilant-cannon-el2lp8/docs/sketch/ja/plan-rearchitecture.md)、discussion #1090）は、計測ではなくコードから推論した主張をいくつか前提にしている。そのうちどれが本当に成り立つのか。

英語版: [../spike-rearchitecture.md](../spike-rearchitecture.md)。

## 要約

| # | 計画の主張 | 判定 | 計画をどう直すか |
|---|---|---|---|
| S1 | C1: コンパイルは `Indexed` までしか進めず、`Initializing` には進めない | **成立**（違反 0 件） | 変更不要。計画どおり強制できる |
| S2 | C3: コンパイル時の答えが *unknown* でも常に安全で、`WithStrictLazyCompile` でも結果は同一になる | **不成立** | コンパイル時の知識を *ヒント* と *事実* に分ける。事実は答えるか実行時に遅延させる。捨ててはいけない |
| S3 | `chunkCellsEscape` はほぼすべての chunk で true で、原因はストア先である | **誇張**（実際は 45%。主因はフィールド読み出しとレシーバ） | スロット分類は selector の読み出しと呼び出しレシーバも対象にする。`OpNewLocal`/`OpRenewVar` の毎回のアロケーションをやめる |
| S4 | スロットを分類すれば大半の chunk が escape しなくなる | **成立**（45% → 約 10%。Boxed になるローカルは約 5%） | 特殊形式の呼び出し地点から見えるローカルはすべて Boxed にする必要がある |
| S5 | `VMCaller` は小さな capability インタフェースに分割できる | **成立。ただし表面は計画の列挙より広い** | インタフェースアサーション経由の隠れた capability が 5 つある。184 関数は caller を受け渡すだけ |
| S6 | 全エンジンが `installStdlib` を払っているので、パッケージ単位の遅延 install には価値がある | **一部成立** | `NewSession` では大きいが、`NewEngine` では小さい（`go env` のサブプロセスが支配的）。扱うべき結合が 4 つある |
| S7 | `reflect.Type` 1 つにつき `*types.Type` を 1 つにすれば、twin/shape 系のヒューリスティクスはすべて `==` になる | **一部成立** | キーは `reflect.Type` ではなく、表している型にする。9 つの helper のうち `==` になるのは 5 つで、残る 4 つは Go の型同一性規則そのものを実装している |

## S1, S2: コンパイル時の遅延性の契約（C1〜C3）

### 方法

- `compile/compile.go` にある 8 箇所の `ref.Materialize()` を 1 つの関数（将来の oracle）に集める。環境変数でモードを切り替える。
  - `off`: 挙動は変えず、最初の materialize を記録するだけ。
  - `none`: 一切答えない。C3 のもっとも強い形にあたる。
  - `peek`: 他の誰かが既に materialize した ref にだけ答える。計画の `WithStrictLazyCompile` にあたる。
- `Engine.buildPackage` が `minigo/compile.` のフレームを含むスタックから呼ばれたら `compile-build` を記録する。`EnsureReadyRun`/`EnsureConstsRun` がそうしたスタックから bootstrap を始めたら C1 違反として記録する。
- 各モードで `go test ./... -count=1` と全 examples を走らせ、失敗したテスト名を `off` と比較する。どのモードでも失敗する 3 件（`os_err_script_path` と `TestREPLLoadErrors` は `permission denied` を期待している）は、サンドボックスが root で動いていることが原因なので除外した。

### 結果

- **C1 は成立する。** スイート全体と examples を通して、コンパイルのスタックから初期化が始まったことは一度もなかった。
- コンパイル起因のロードは実在するが、多くはない。root モジュールでは、パッケージのビルド 2,502 回のうち 162 回（6.5%）がコンパイラから始まっていた。大半は小さな host 側のパッケージ（`io/fs`、`mime/multipart`、`net/http`、`net/url`、`math/bits`）だった。
- **C3 は成立しない。** `none` で 15 件、`peek` で 13 件、新たに失敗した。

| 種類 | テスト | 原因 |
|---|---|---|
| 誤った出力（I5 違反） | `assign_pkgname_differs`、`litkey_named_peel`、`src_pkgtype_maplit_keys`、`shift_const_count`（`none` のみ） | 複合リテラルのキーの型付け、パス末尾とパッケージ名が異なるパッケージを経由する代入、シフト回数の定数性フラグ（`ShiftConstCount`）が、いずれも import 先パッケージを読んでいる |
| 偽の trap | `TestNetHTTPRoundtrip`（`none` のみ） | メンバの宣言型が分からないため比較が interface 比較に下ろされず、`rc == NoBody` が `mismatched types io.ReadCloser and noBody` で trap する |
| gc なら拒否するプログラムが通る | `gate_*` 7 件、`selconv_typearg`、`selnilable_trap` | gc が `mismatched types time.Duration and int64` などで拒否するところで、実行が成功してしまう |
| ユニットテスト | `compile.TestSelMemberSyntheticTypeEntry` | 検索関数そのものを直接テストしている |

- `peek` でも 13 件が壊れる。つまり「すでに `Indexed` のものにだけ答える」方式では、結果がロードの順序に依存する。ロード順に依存するモードは、CI の同値性チェックには使えない。
- 根本原因: 現在のコンパイル時の問い合わせは、高速化のためのヒントだけではない。一部は意味論を決めている。具体的には、定数性（`isConstExpr`、`certainlyNonConst`）、修飾された型が nil になり得るか（`selNilable`）、等値比較の下ろし方を決める静的なオペランド型、リテラルのキーの型である。

### 計画をどう書き直すか

- **C3′.** コンパイル時の知識には 2 種類ある。
  - *ヒント* は速くするだけのものなので、捨てても安全である。
  - *事実* は意味論を決める。事実は、`Indexed` までロードして答える（C1 の範囲内）か、VM が最初の実行時に解決する遅延チェックとして出力する。遅延チェックは `CallSite` に `types.Ref` を持たせる。どちらかでなければならない。
- そうすれば `WithStrictLazyCompile` は「事実をすべて遅延させる」モードになり、意味のある同値性テストになる。「oracle が unknown を返す」モードは同値性テストにならない。
- フェーズ 3 のステップ 5 は、8 箇所の呼び出し（と、コンパイラが `DisplayName` 経由で到達する `runtime/typedef.go` の呼び出し）をヒントと事実に仕分けるところから始める。

## S3, S4: スロット分類

### 方法

`OpLocalRef` を出力するたびに、その理由を chunk のサイドテーブルに記録する。VM が最初に `chunkCellsEscape` を計算した時点で chunk を記録する。さらに、ポインタレシーバがフレームローカルの cell を束縛した事象を実行時プローブで記録する。対象は root のスイートと examples 6 つで、重複除去後 4,161 chunk（生の数で 11,402）。

### 結果

- 現状 `chunkCellsEscape` が true の chunk: **44.5%**（ローカルを持つ chunk に限れば 56%）
- `OpLocalRef` の理由の内訳:

| 理由 | 割合 | escape するか |
|---|---:|---|
| selector の読み出し `x.f` / メソッド値 `x.M` | 35.9% | しない。ただしポインタレシーバのメソッド値は除く |
| 呼び出しのレシーバ `x.M()` | 34.7% | ポインタレシーバが cell を束縛するときだけ（実行時に観測されたのは 31 chunk） |
| `x = v`、`x.f = v`、`x[i] = v`、多重代入、`op=`、`*p = v`、incdec | 約 27% | しない |
| `&x`、`&x.f`、`&x[i]` | 2.2% | する |

- 正確に分類した場合に escape する chunk の割合:

| 基準 | escape する chunk | Boxed になるローカル |
|---|---:|---:|
| `&`、クロージャによるキャプチャ、特殊形式のみ | 9.3% | 4.7% |
| ＋実行時に観測されたポインタレシーバの束縛（**現実的な値**） | **9.7%** | **5.0%** |
| ＋`x.M()` のレシーバをすべて escape とみなす（悲観的な上限） | 27.9% | 15.1% |

### 計画の修正点

1. 主因はストアではない。`selectorBaseIsVar` が任意の変数に対して true になるため、`OpLocalRef` の約 70% はフィールドの読み出しと呼び出しのレシーバから来ている。フィールドの読み出しは `OpLocal` + `OpSelect` にする。`x.M()` ではレシーバの種類を知る必要がある。静的に（S2 でいう *事実* として）求めるか、実行時のフォールバックで判断する。
2. escape しない chunk で節約できる量は、計画の想定より小さい。`OpNewLocal`（vm.go:2597）は `:=`/`var` のたびに新しい `Cell` を確保し、`OpRenewVar`（vm.go:2607）はループの反復ごとに 1 つ確保する。そのため #1024 のブロック再利用が効くのは引数だけである。Plain スロットでは、この 2 つの op がその場に書き込むように変える必要がある。
3. 特殊形式: `trySpecial`（compile.go:6641）は、その地点から見えるローカルをすべて `QuotedCall.Locals` にスナップショットし、`specialCtx.Eval`（vm.go:17576）は呼び出し元の cell を共有する。したがって特殊形式の呼び出し地点から見えるローカルはすべて Boxed にする必要がある。該当する chunk は 25 個だけだった。
4. フレームの cell を汎用的に読んでいて、移植が必要な箇所:
   - `selectMember` / `structMember` の `case *Cell`（vm.go:3640, 7552）
   - `assignRef`（vm.go:1156）
   - `specialCtx.Resolve`（vm.go:17409）
   - `localTypedefOf`（vm.go:8886）
   - 名前付き戻り値（vm.go:2182, 3356）
   - range のイテレータのスロット（vm.go:3218）

   いずれもブロッカーではない。`OpEvalAST`、`goto`、`defer`/`recover`、`inspect` は影響を受けない。

## S5: `VMCaller` の分割

### 方法

`go/ast` で走査した。対象は、`VMCaller` を引数に取るテスト以外のすべての関数と関数リテラルで、その引数に対してどのメソッドを呼んでいるかを数えた。

### 結果

- `VMCaller` を受け取る関数は 360 個。
  - 96 個は一切使っていない。
  - 184 個は helper（`runtime.IfaceMember`、`hostErrOf`、`display`、`callOrPanic`、`goJSONSeen` など）に受け渡すだけ。
  - 80 個がメソッドを直接呼んでいる。
- メソッドごとの呼び出し関数数: `Call` 24、`TypeOf` 16、`Convert` 13、`ElemZero` 12、`Zero` 10、`ElemOf` 6、`Copy` 5、`Equal` 4、`ResolveType` 4、`NewMap` 3、`Member` 3。残りは 2 以下。`Task` はテスト以外から呼ばれていない。
- **隠れた表面。** 呼び出し側は `VMCaller` を無名インタフェースへ型アサーションして、`ElemZero`（3 箇所）、`HostClear`、`HostMapDelete`、`MaterializeConstErr`、`HostNilTwin`、`CallCatching` を使っている。さらに `vm.vmOf`（vm.go:4994）は 7 箇所で `*VM` にダウンキャストしている。

### 判定

- 分割は可能であり、大半の bind は何も要らないか、`Caller` だけで足りる。
- ただし capability の集合には隠れたアサーションも含める必要がある。そうしないと、bind を root の外へ出しても具象の `*VM` を引きずることになる。
- 184 関数が本当に何を必要とするかは受け渡し先の helper で決まる。そこで、まず helper の引数を capability 型にする。

## S6: パッケージ単位の遅延 stdlib install

### 計測

4 CPU、他の処理と共有していてノイズの多いマシン。

| ベンチマーク | time/op | B/op | allocs/op |
|---|---|---|---|
| `installStdlib` 単体 | 0.76–0.87 ms | 425 KB | 4,203 |
| `NewEngine` | 6–11 ms | 566 KB | 4,437 |
| `NewSession` | 0.57–1.0 ms | 440 KB | 4,273 |
| `NewEngine` 経由の最小の実行 | 11–14 ms | 593 KB | 4,655 |
| `NewSession` 経由の最小の実行 | 1.1–1.4 ms | 451 KB | 4,429 |

- **エンジン経路。** `installStdlib` は実行時間の約 7% にすぎないが、アロケーションでは 90% を占める。実行時間を支配しているのは、`pkg/locator` がエンジンごとに起動する 2 つの `go env` サブプロセス（`GOROOT`、`GOMODCACHE`）である（locator.go:133–138, 474–490）。これをメモ化するほうが安く、効果も大きい。
- **セッション経路。** `installStdlib` が実行時間の 60〜70%、アロケーションの約 94% を占める。ここでは主張が成り立つ。
- スイートが bind しているのは 45 の import パスと 1,134 のメンバ。testdata と examples のファイルが直接 import している bind 済みパッケージは、平均 1.92 個。

### 遅延レジストリが扱うべき結合

1. `hostTwin`（minigo.go:314）は、*他の* パッケージから流れてきた host 値（`ctx.Deadline()` が返す `time.Time` など）について、`e.pkgs[rt.PkgPath()]` から bind 済みの typedef を探している。遅延にするなら、この検索でも install を起動しなければならない。そうしないと黙って nil が返る。
2. `BoundPackages`/`BoundSymbols`/`BoundOf`、`vet.go:196`、`SourceOf`、`NewSession` の bind の優先順位、`loadPathMode` の bound-shadow チェックは、いずれも `e.binds`/`e.pkgs` を即時に読んでいる。構築済みのテーブルとは別に、名前だけのレジストリが必要になる。
3. クロージャがエンジンの状態をキャプチャしている: `e.fsPath`（17 箇所）、`e.cwd`（`os.Chdir` が書き込む）、`e.args`、出力先、そして `os` と `host` のテーブルの中身を決める `AllowedRoots`。そのため遅延にしても、テーブルはエンジンごとに作り直しになる。セッション間で共有したいなら、これらの状態を呼び出しコンテキストへ移す必要がある。
4. グローバルな `templateFuncMap` の TypeDef は全エンジンで共有されている。`Bind` は `td.Pkg` が nil のときだけ設定する（minigo.go:787）ので、最初のエンジンのパッケージへのポインタが後続のすべてのエンジンに残る（TODO.md に記録した）。

### 生成

- 869 エントリのうち約 50〜55% は、`gen-intrinsics` で生成できる形をしている。
- 約 12% は、メソッドリストや `HostScalar` を持つ typedef で、現在の生成器はこれを出力できない。
- 約 25% は本物のアダプタである。
- `WrapFunc` は呼び出しのたびに reflect を使うので、生成した bind は現在の直接ラッパより遅くなる。I6 の下では許容範囲だが、計画に明記すべきである。

## S7: host 型の intern

### 結果

- 「`reflect.Type` 1 つにつき `*TypeDef` 1 つ」が成り立たない主な理由は、bind がエンジンごとに作り直されていることにある。エンジンとセッション 1,035 個を通して、99 個の host 型がエンジンごとに新しい TypeDef を得ていた。
- 1 つのエンジンの中での重複は、キャッシュのない実行時の合成から生じている。
  - `vm.hostTypedefOf`（vm.go:18295）と `hostPkgElemTypedef`（vm.go:18347）は、呼び出しのたびに新しい TypeDef を作る。`ast.Expr` の合成コピーは 163 個あった。
  - bind 済みの TypeDef と合成された TypeDef の両方を持つ型が 14 個あった。
  - source 側で宣言された twin と合成された TypeDef の両方を持つ型が 26 個あった。
- twin は、1 つのパッケージが bind と source 解釈の両方で同時に存在することから生じるのではない。bind されたパッケージが返す値の型が、source 解釈されているパッケージで宣言されている、という状況で生じる。たとえば、`io/fs` が source 解釈のときに `os` が `*fs.PathError` を返す場合や、host の `go/ast` ノードが source 解釈の `go/ast` と出会う場合である。型を統合するのは正しい。しかし値の表現（`*runtime.Struct` と `GoValue`）は異なるままなので、変換層（`hostErrTwins`、`goNative`、プロキシのボックス）は残る。
- **`reflect.Type` をキーにすることへの反例。** `atomicBox` は `atomic.Value` と `atomic.Pointer` の両方を裏で支えており、`timerChanBox`/`afterFuncTimer` は `*time.Timer` を表している。intern のキーは、ボックス自体の Go の型ではなく、*表している* 型（PkgPath, Name）にしなければならない。

| helper | `==` になるか |
|---|---|
| `hostTwinIs`、`hostNilTwin`、`sameSpelledTwin`、`HostNamedIs`/`HostTwinOf`、`sourceTwinOf` | なる。エンジン単位で (PkgPath, Name) をキーに intern し、合成もそこを通す場合に限る |
| `peelAlias` | 生成時に alias を解決する場合に限りなるが、それは遅延性と衝突する |
| `tdShapeEq`、`underlyingShape`/`convShapeEq` | ならない。異なる型同士について、Go の「underlying が同一」の規則を実装している |
| `structFieldsEq` | ならない。完全な構造的 hash-consing をしない限り無理 |

## このスパイクから導かれる次の作業（順に）

1. `pkg/locator` で `go env GOROOT`/`GOMODCACHE` をメモ化する。小さく、他と独立しており、`NewEngine` の単独で最大のコストである。
2. 計画 §2.4 を C3′（ヒントと事実の区別）に書き換え、`WithStrictLazyCompile` を「事実をすべて遅延させる」モードと定義し直す。
3. 計画 §2.5.1 を書き換えて、selector の読み出しとレシーバも分類の対象にする。Plain スロットでは `OpNewLocal`/`OpRenewVar` がその場に書き込むようにする。
4. 隠れたアサーションを §2.2 の capability 一覧に加え、受け渡し先の helper から先に型付けする。
5. 計画 §2.3 を、エンジン単位で表している型の (PkgPath, Name) をキーに intern する形に書き換え、「twin/shape 系のヒューリスティクスがすべて消える」という主張を取り下げる。
6. S6 で挙げた遅延レジストリの 4 つの結合を §2.7 に加え、遅延 install の根拠を `NewSession` のコストに置く。
