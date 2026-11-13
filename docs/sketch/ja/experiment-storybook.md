# storybook: minigo で net/http のレンダリングを Storybook 風に覗く実験

対象: `examples/storybook`（実験。マージされるかは未定）
問い: minigo の「任意のエントリーポイントを呼べる」機構とインタプリタ自身を使って、net/http ハンドラーのレンダリング部分を、ビルドタグで本物の Go から隔離したコードブロックからダミー値つきで呼び出し、HTML の見た目を確認できるか。足りない binding は何か。

---

## 1. 総評

**できる。** 小さなアプリ（`testdata/app`）で、ストーリー 5 本すべてが minigo 上でレンダリングでき、ネイティブ Go で同じストーリーを実行した結果（golden）とバイト単位で一致した。ブラウザで一覧・プレビューでき、ソースを編集するとリロードだけで反映される（アプリのビルドは不要）。

ただし素の minigo のままでは動かず、次の 3 点を足した。

1. **`html/template` の host binding**（最大の障害。従来は init 時に trap して一切使えなかった）
2. **boxed host map/slice の index**（`r.URL.Query()["tag"]` が trap していた）
3. **CLI の `--tags`**（ビルドタグ付きファイルをエントリーポイントとして選ぶため）

エントリーポイント機構そのもの（`Engine.Run` / `Engine.Call` / `minigo run --entry`）は既存のもので足りた。

素の Go で同じことをやる場合との比較は [7 章](#7-minigo-を使わず-go-だけでやる場合との比較)。結論だけ書くと、優位性は「速さ」より「ビルドを通さずに名前で呼べること」にあるが、現状の忠実度の穴を考えると十分とまでは言えず、いまのところは動作検証寄り。

## 2. 仕組み

### 2.1 ストーリーの書き方

```go
//go:build story

package app

// StoryUser renders the user page with a fixed user.
func StoryUser(w http.ResponseWriter, r *http.Request) {
	RenderUser(w, users["1"])
}
```

- ストーリーは「タグで選ばれ、素の `go build` では落ちるファイル」にある、exported なトップレベル `func StoryXxx(w http.ResponseWriter, r *http.Request)`。
- シグネチャを `http.HandlerFunc` と同じにしたので、ストーリーはレンダリング関数（`RenderUser`）を直接呼んでも、ハンドラー全体（`(&Handler{Store: fake}).ShowUser(w, r)`）を偽の依存込みで呼んでもよい。`r.SetPathValue` でパスパラメータも差し込める。
- ダミー値（`fakeStore` など）もストーリー側のファイルに置くので、本番バイナリには一切入らない。`go vet .` と `go vet -tags story .` の両方が通る。
- `Story` で始まるのにシグネチャが違う関数はエラーにする（黙って無視しない）。

### 2.2 実行

`storybook` は `go/build.Context.MatchFile` でタグあり/なしを比較してストーリーファイルを見つけ、`go/parser` で `Story*` 関数を列挙する（`go/packages`・`go list` は使わない）。

レンダリングは 1 ストーリーごとに

1. `minigo.NewEngine(dir, WithBuildConfig(resolve.BuildConfig{Tags: []string{tag}}))` でパッケージを解釈
2. host 側で `httptest.NewRecorder()` と `httptest.NewRequestWithContext(...)` を作る
3. `Engine.Call(ctx, pkg, "StoryXxx", &runtime.GoValue{V: rec}, &runtime.GoValue{V: req})`
4. recorder から status / header / body を取り出す

という流れ。`w` と `r` は host の値のまま渡るので、`w.Header().Set`、`http.Error`、`r.URL.Query()`、`r.SetPathValue` などは host の net/http がそのまま処理する。

CLI:

```console
$ go run . -list ./testdata/app
$ go run . ./testdata/app StoryUser
$ go run . -o out ./testdata/app       # index.html + ストーリーごとの .html
$ go run . -http :6006 ./testdata/app  # 左にナビ、右に iframe
```

`-http` モードはリクエストごとにエンジンを作り直してパッケージを解釈し直す。`handler.go` のテンプレートを書き換えて curl すると、再起動なしで変更後の HTML が返ることを確認した。クエリ文字列はストーリーに転送されるので `/story/StoryUserFromQuery?name=Carol&tag=a` が Storybook の controls 相当になる。

## 3. 結果

| ストーリー | 内容 | 結果 |
|---|---|---|
| `StoryUser` | 固定ユーザー、`template.HTML` のバッジ、`{{.Initial}}`（スクリプト側メソッド）、`{{range}}` | 200, golden 一致 |
| `StoryUserEscaped` | 名前が `<script>alert(1)</script>` | エスケープされる。golden 一致 |
| `StoryUserFromQuery` | `?name=Bob&tag=x&tag=%3Cy%3E` | 200, `<li>&lt;y&gt;</li>`。golden 一致 |
| `StoryShowUser` | ハンドラー全体を fake store で | 200, golden 一致 |
| `StoryShowUserNotFound` | エラー経路 (`http.Error`) | 404 `text/plain`, golden 一致 |

- `book_test.go` は minigo で全ストーリーを描画して `testdata/golden` と比較、`oracle_story_test.go`（`-tags story`）は同じストーリーをネイティブ実行して同じ golden と比較する。Makefile の `test` に両方を追加した。
- 1 ストーリーの描画（プロセス起動込み）は約 0.07 秒 / 約 42MB。

## 4. 見つかった不足と対処

### 4.1 `html/template` が init で trap する（対処済み）

```text
runtime trap: undefined: template.FuncMap
File "/usr/local/go/src/html/template/escape.go", line 65, in template.__init__()
```

`html/template` はソース解釈され、その中の `var funcMap = template.FuncMap{...}` が、host binding 版の `text/template` に `FuncMap` が無いため落ちていた（TODO に既出）。net/http のビュー層ではほぼ必ず使うので、これが最大の障害だった。

対処: `text/template` と同じく `html/template` を host binding にした（`intrinsics.go`）。`Template` / `New` / `Must` / エスケープ関数に加え、`HTML` / `HTMLAttr` / `JS` / `JSStr` / `CSS` / `URL` / `Srcset` を **host のスカラー named type** として bind したので、スクリプト側の `template.HTML("<b>..</b>")` が host の escaper に「信頼済み」として届き、エスケープされない。`Must` は text/template と共通の `templateMust` に寄せた。`testdata/htmltmpl` で、文脈依存エスケープ・スクリプト側メソッド呼び出し・`template.HTML`・`Must` の panic をネイティブ Go と同じ出力で固定した。

残り: `template.FuncMap` は text/template・html/template の両 binding で未定義のまま（スクリプトの関数を host テンプレートの FuncMap に渡す変換が要る）。`--src html/template` も同じ理由で動かない。TODO の既存 entry に残した。

### 4.2 `r.URL.Query()["tag"]` が trap する（対処済み）

```text
runtime trap: index on *runtime.GoValue
```

`url.Values` は host の named map なので、メソッド（`.Get`）を呼べるよう `GoValue` の箱のまま渡ってくる。`len` は箱の中身を測れるようになっていたが、index は `Map`/`Slice`/`Named` しか扱っていなかった。単体の `url.ParseQuery` では再現しない（その経路では箱にならない）ため、最小化に手間取った。

対処: `VM.index` / `VM.indexOK` に、`GoValue` の中身が map/slice/array なら reflect で読むフォールバック（`VM.hostIndex`）を足した。comma-ok（`v, ok := q["x"]`）も効く。副作用として既存 TODO の `net.IP` の `ip[3]` も通るようになった。slice 式と range は未対応のままなので、TODO entry を部分完了 `[-]` にした。

読み取り以外はまだ trap する（追加で試した結果、TODO に新規 entry として記録）:

| 書き方 | 結果 |
|---|---|
| `r.URL.Query()["tag"][:1]` | OK（要素の `[]string` は箱から出る） |
| `var q url.Values = r.URL.Query(); q.Get("tag")` | OK |
| `for k, v := range r.URL.Query()` | `range over *runtime.GoValue` |
| `w.Header()["X-A"] = []string{"1"}` | `index assign on *runtime.GoValue` |
| `q := r.URL.Query(); q["z"] = ...` | 同上 |

`w.Header()` への直接代入はハンドラーでよく書くので、実アプリに当てると最初に踏みそう。

### 4.3 タグ付きファイルをエントリーポイントにできない（対処済み）

`Engine` には `WithBuildConfig` があったが、CLI からタグを指定する手段がなかった。`minigo run --tags story ./app StoryUser`（`--tags=a,b` 形式も可）を追加した。storybook 自体は API 経由なのでこれを使わないが、ストーリーを素の minigo で 1 本叩くのに便利。

### 4.4 net/http 側の binding

今回の範囲（`ResponseWriter`・`Request`・`Header`・`http.Error`・`SetPathValue`・`ServeMux` 登録）では **追加は不要だった**。いずれも host の値として扱われるため。`-src net/http` でも 5 本とも描画できた。

## 5. 気づいたこと・限界

- **パッケージ単位で解釈する。** ストーリーファイル単体を参照すると同じパッケージの `RenderUser` が見えない（`undefined: RenderUser`）。ディレクトリを渡す必要がある。
- **アプリ側の書き方に依存する。** レンダリングが `func(w, data)` のように切り出されていれば素直に呼べる。DB クライアントなどを直接握るハンドラーは、interface で受けていないと fake を差し込めない（これは Go のテスト容易性と同じ条件）。
- **ネイティブとの一致は golden で担保している。** minigo の解釈結果が Go と食い違うと、見た目のプレビューが嘘をつく。`oracle_story_test.go` のように、同じストーリーをネイティブでも実行して比較する仕組みをセットで持つのがよい。
- minigo が binding を持たないパッケージ（サードパーティのテンプレートエンジン等）をソース解釈すると、今回の `html/template` のように init で落ちる可能性がある。その場合 trap のトレースバックでどの宣言が原因かは分かる。

## 6. 今後やるなら

- `template.FuncMap` の binding（スクリプト関数を host 関数に変換）。実アプリのテンプレートはほぼ FuncMap を使う。
- ストーリーに引数（ダミー値）を外から与える仕組み。今は `?query` を読む程度だが、例えば `func StoryUser(w, r, u *User)` のように型付きの controls を JSON から組み立てる案がある。
- `-http` で、ファイル変更を監視してブラウザを自動リロードする。

## 7. minigo を使わず Go だけでやる場合との比較

minigo の存在価値は「Go でもできることを、Go より安く・制約少なくできるか」で決まる。素の Go での代替案を実際に組んで測った。

### 7.1 Go での代替案

- **A. 同じパッケージのテストから呼ぶ**: `story_user.go` はそのままにして、`go test -tags story` のテスト（`oracle_story_test.go` と同じ形）でストーリーを呼び、HTML をファイルに書くか比較する。追加の道具は要らず、結果はネイティブそのもの。
- **B. レジストリ付きの main を生成して `go run -tags story`**: `map[string]http.HandlerFunc{"StoryUser": app.StoryUser, ...}` を持つ main を go/parser で生成し、ビルドして配信する。ブラウザで見るには、ファイル変更を監視して再ビルド・再起動する仕組み（air のようなもの）を足す必要がある。
- **C. plugin**: Linux/macOS 限定で、ツールチェーンとビルドフラグが完全に一致している必要があり、一度ロードすると外せない（更新するにはプロセスの再起動が要る）。試していない。

A と B を `examples/storybook/testdata/app` で組んで測った（8 コア、go1.27.1、ビルドキャッシュあり）。

### 7.2 測定

| 操作 | 時間 |
|---|---|
| B: GOCACHE が空の状態からの初回 `go run -tags story` | 5.66 秒 |
| B: 変更なしでの再実行 | 0.06 秒 |
| B: `handler.go` を編集した後の `go run` | 0.32 秒（`go build` だけなら 0.39 秒、バイナリ 10.8MB） |
| A: `handler.go` を編集した後の `go test -tags story -run TestNativeGolden` | 0.55〜0.60 秒 |
| minigo: 1 ストーリーの描画（プロセス起動込み） | 0.07 秒 |
| minigo: `-http` サーバーでの 1 リクエスト（パッケージの再解釈込み） | 0.06〜0.07 秒 |

パッケージが大きい場合も見るため、アプリに 2000 個のメソッド（約 2 万行）を足した合成パッケージで測った。

| 操作 | 時間 |
|---|---|
| B: `handler.go` を編集した後の `go run` | 0.96 秒 |
| minigo: 1 ストーリーの描画（プロセス起動込み） | 0.08〜0.09 秒（約 50MB） |

観測できたこと: Go は編集したパッケージ全体を機械語に再コンパイルし、バイナリ全体をリンクし直すので、パッケージが大きくなるほど編集ループが伸びる。minigo は同じ 2 万行でも 0.02 秒ほどしか増えなかった。minigo は VM のインタプリタで、機械語へのコンパイルもリンクもしない。パッケージは必要になった時点で構文解析と索引づけだけを行い、関数は最初に呼ばれたときにバイトコードへ変換して VM で実行する（`minigo.go` のパッケージドキュメント）。そのため、呼ばれない関数にはほとんどコストがかからない。依存の多い実アプリでは Go 側のコンパイルとリンクがもっと重くなるはずだが、これは測っていない推測。

### 7.3 制約の違い

| 観点 | Go（A / B） | minigo |
|---|---|---|
| 名前でストーリーを呼ぶ | A は `-run`、B はレジストリのコード生成が要る（Go の reflect ではパッケージの関数を名前で引けない） | `Engine.Call(pkg, "StoryXxx")` で直接呼べる。生成物がない |
| ハンドラーが `package main` にある | B は不可（`import "im/srv" is a program, not an importable package` を確認）。A なら可 | 可（`package main` に変えたコピーで描画できることを確認） |
| 同じパッケージに編集途中のコードがある | パッケージ全体がビルドできない（`declared and not used` や型エラー、未定義の識別子など。`wip.go` を足して確認） | 呼ばれない関数の型エラーは無視されて描画できる（同じ `wip.go` で確認）。プレビューとしては長所だが、エラーを見逃す原因にもなる |
| 再読み込み | 再ビルドして、サーバーなら再起動する。状態は消える | サーバーのプロセス内でリクエストごとに解釈し直す。再起動しない |
| 忠実度 | ネイティブそのもの | 本書 4〜5 章の穴がある（`template.FuncMap`、`w.Header()[k] = v`、host の map の `range` など）。一致を保証するにはネイティブとの比較（＝ Go）が結局必要 |
| 必要な環境 | Go ツールチェーンとビルドキャッシュ | storybook のバイナリとソース（ソース解釈するパッケージは GOROOT/モジュールのソースを読む） |
| 依存パッケージの init | 実行される | 実行される（違いはない） |

### 7.4 評価

- **速さの優位は、小さなアプリでは薄い。** Go の再ビルドは 0.3〜0.6 秒で、人間の編集ループの邪魔にはほとんどならない。差が意味を持つのは、パッケージが大きく編集ループが秒単位になる場合（合成パッケージで 0.96 秒 対 0.09 秒）。依存の多い実アプリではさらに差が開くと見込まれるが、未測定。
- **構造的な優位はある。** (1) レジストリの生成もテスト関数も書かずに名前で呼べる、(2) `package main` に直接書かれたハンドラーをそのまま呼べる、(3) 同じパッケージに壊れたコードがあっても見た目だけ確認できる、(4) 常駐サーバーで再起動なしに反映できる。どれも Go 単体では工夫が要るか、できないこと。
- **ただし忠実度の穴が優位を相殺している。** 実際のビューのコードはほぼ確実に `template.FuncMap` を使い、ハンドラーは `w.Header()` に直接書く。どちらも今の minigo では trap するので、実アプリに当てると構造的な優位を活かす前に止まる可能性が高い。さらに、表示が正しいことを保証するには A の形でネイティブと比較するしかなく、Go を使わずに済むわけではない。
- **結論**: いまの段階では「Go に対して十分な優位性がある」とは言えず、**主に動作検証**の位置づけ。存在価値の候補になるのは「大きい・壊れている・`package main` のコードを、ビルドせずに名前で叩いて覗く」という用途に絞った場合で、そのためには少なくとも `template.FuncMap` と、host の map への代入・`range` を埋める必要がある（いずれも TODO に記録済み）。埋めた後に、依存の多い実アプリで 7.2 の編集ループを測り直すのが次の判断材料になる。

## 8. 追補: host の map/slice への書き込みと range（stack 1 層目）

7.4 で「実アプリに当てると最初に踏みそう」とした穴のうち、host の named map/slice（`url.Values`・`http.Header`・`net.IP`）を箱のまま扱う経路をまとめて埋めた。

| 書き方 | 以前 | 現在 |
|---|---|---|
| `w.Header()["X-A"] = []string{"1"}` / `q["z"] = v` | `index assign on *runtime.GoValue` | host の map に書く（`VM.hostSetIndex`） |
| `for k, v := range r.URL.Query()` | `range over *runtime.GoValue` | キーを snapshot し値は都度読む（iterator kind `'h'`）。到達前に消したキーは出ない |
| `q["tag"][0] = v` | 黙って失われる（script 側のコピーに書く） | host の要素の backing に書く（`VM.hostElemRef`） |
| `delete(q, k)` / `clear(q)` | `delete on *runtime.GoValue` | reflect で消す |
| `ip[:2]` / `ip[1:3:4]` / `range ip` / `ip[0] = 9` | `slice on` / `range over` | 動く。`ip[:2]` は `net.IP` のまま（`%T` も一致） |

境界外アクセスは script の slice と同じ runtime error を出す。`testdata/hostboxed` で、ネイティブ Go の実行結果と同じ文字列を `TestHostBoxedWrites` で固定した。

### 8.1 example を実アプリ寄りにした

`testdata/app` に `ListUsers` ハンドラー（`?tag=` で絞り込み、`?sort=` で並べ替え、件数を `X-Total-Count` ヘッダーで返す）を足した。書き方は素の Go のハンドラーとして自然なものにしている。

- `q := r.URL.Query(); if _, ok := q["sort"]; !ok { q["sort"] = ... }` で既定値を入れる
- `for k, vs := range q` で有効なクエリを画面に出す
- `w.Header()["X-Total-Count"] = ...`
- `slices.ContainsFunc(u.Tags, ...)` でタグを照合する

ストーリーは `StoryListUsers` と `StoryListUsersByTag`（`/?tag=go&sort=id`）。golden は Content-Type だけでなく **全ヘッダー** を比較するようにした（`X-Total-Count` や `http.Error` が付ける `X-Content-Type-Options` もネイティブと一致する）。

ここで新たに `slices.ContainsFunc` が未 binding（`undefined: slices.ContainsFunc`、TODO の未 binding 一覧に既出）で、`slices.IndexFunc` も nil slice を渡すと `first arg must be a slice` で落ちることが分かった。前者を足し、両方を nil slice で -1 / false を返すよう共通化した。実アプリ寄りに書き直すと、こうした「小さいが確実に踏む」未対応が出てくる。

残り: `template.FuncMap`（次の層）、host の構造体フィールド経由の slice 書き込み（`ce.Args[0] = v`、TODO 既出）、nil の named host map が typed nil にならない件（TODO に分離）。
