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
