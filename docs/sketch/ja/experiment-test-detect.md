# test-detect: エラー UX 実験 — 「入力側の語彙で返す」度合いを測る

対象: `examples/test-detect`（[plan-test-detect.md](../plan-test-detect.md) / PR [#397](https://github.com/podhmo/minigo/pull/397) のブランチ上）
問い: 日常の開発で起きうる「ずれた入力」を与えたとき、このツールは「どこが、なぜ、誰のせいで壊れたか」を入力側の語彙で返すか。そして、それができないときに成功したふりをしないか。

> 「エージェントに親切なツールとは、直接書く場合に得られていたフィードバック（どこが、なぜ、誰のせいで壊れたか）を、入力側の語彙で返し直してくれるツールだと考えます。そして、それができないときに、成功したふりをしないツールです。」

方法: 実リポジトリ（podhmo/minigo 自身）と、合成した 2 モジュールの fixture repo（`example.com/m` の a<-b<-c と `example.com/sub`）に対し、約 70 の壊れた操作を打ち、stdout/stderr/終了コードを採取した。評価語彙は kind（入力の語彙で where/why/whose-fault が返る）/ acceptable（ラウドだが改善余地あり）/ misleading（誤解を招く）/ silently wrong（成功のふり）/ crashy。

---

## 1. 総評

**スキャン側（ツリーが壊れている側）は設計どおり強い。** パース断片・go.mod 破損・権限エラーはすべてラウドで、復元した import を捨てない。「黙って失敗しない」は守られている。

**入力側（changed path の解釈）が弱い。** 発見の重心はこちらで、2 つの症状に集約される。

- **ルートパッケージ磁石**: `Dir(input)` が `-root` に潰れる入力（basename だけの `foo.go`、Unix 上の `a\b.go`、ディレクトリ名 `x.go`、サブディレクトリからの cwd 相対パス）は、root にパッケージがあると**警告なしに root パッケージを seed する**。出力は空ではなく、もっともらしいが別パッケージの影響セット。
- **非 `.go` 入力の無警告スキップの射程が広すぎる**: `README.md` だけでなく、ディレクトリや `./...` も同じ「黙って捨てる」に入る。

どちらも「git diff のパイプ入力」という正規利用では発生しないものの、人間やエージェントが手で打つと静かに嘘をつく。

## 2. プローブ結果

### 2.1 壊れた入力（ファイルパス）

fixture repo（root には .go ファイルを置かない構成）での結果。`R` は `-root .` かつ cwd=repo root。

| # | 操作 | 期待される feedback | 実際の挙動 | 評価 |
|---|---|---|---|---|
| i1 | `gone/g.go`（存在しない dir） | 警告で「見つからない」 | `warning: gone/g.go: not in scanned graph (skipped)`、exit 0 | kind（理由は薄い→F7） |
| i2 | `a/no-such-file.go`（既存 dir 内の削除ファイル） | dir 経由で解決 | `a` の影響セットを出力 | kind（設計どおり） |
| i3 | `a/aa.go`（タイポ） | — | 同上（dir 粒度がファイル名の誤りを吸収） | kind |
| i4 | `a`（ディレクトリを渡す） | 「.go ではない/ディレクトリ」と言う | **完全に沈黙。空出力、exit 0** | **silently wrong**（F1） |
| i5 | `./a`（同上 `./` 付き） | 同上 | 同上 | silently wrong（F1） |
| i6 | `README.md` | 無視（設計） | 沈黙 | acceptable（docs 混入は常態、ただし F1 と同じ経路） |
| i7 | `./a/a.go` | 解決 | 正常 | kind |
| i8 | 絶対パス | 解決 | 正常 | kind |
| i9 | `../outside/x.go`（-root 外） | 「root の外」と言いたい | `warning: ../outside/x.go: not in scanned graph (skipped)` | acceptable（why が薄い→F7） |
| i10 | `/etc/passwd.go`（-root 外絶対） | 同上 | 同上 | acceptable |
| i11 | 空 argv + 空 stdin | 空出力 | 空出力、exit 0 | kind |
| i12 | 重複入力 | 重複除去 | 正常 | kind |
| i13 | `a\a.go`（Windows 風） | 「区切りが違う」と言う | fixture では warning。**実リポでは root pkg を seed（後述）** | **silently wrong**（F2） |
| i14 | `'my dir/f.go'`（実在する空白入り dir） | 解決 | 正常に `example.com/m/my dir` | kind |
| i15 | `x.go` という名の**ディレクトリ** | 「ファイルではない」と言う | `Dir("x.go")=root` に潰れ、warning（実リポなら root seed） | **silently wrong**（F1/F2） |
| i16 | `typo.go`（basename のみ、実在しない） | — | fixture では warning。実リポでは **root pkg を seed** | **silently wrong**（F2、設計上の限界あり） |
| i17 | `./...`（go パターンを誤投入） | 「これはパターンだ」と言う | **沈黙**。空出力 | silently wrong（F1 の変形） |
| i18 | `a/A.GO`（大文字） | 無視 | 沈黙 | acceptable（go ツールも .GO を認めないので一貫） |

### 2.2 stdin / 上流コマンド由来の入力

| # | 操作 | 実際の挙動 | 評価 |
|---|---|---|---|
| s1 | `a/a.go\nb/b.go` | 正常 | kind |
| s2 | 空行・空白行混じり | TrimSpace で吸収、正常 | kind |
| s3 | CRLF | ScanLines が `\r` を除去、正常 | kind |
| s4 | 非 UTF-8 バイト混入 | `.go` で終わらない行として無視 | acceptable |
| s5 | `\x00` バイト入り行 | 同上 | acceptable |
| s6 | `git status --porcelain` 行（` M a/a.go` / `?? x.go` / `R a -> b`） | 各行が warning に落ちる：`warning: M a/a.go: not in scanned graph (skipped)` | acceptable。ラウドだが「porcelain ではなく name-only を使え」のヒントはない（F7 の補強余地） |
| s7 | 64KB 超の 1 行 | `test-detect: reading stdin: bufio.Scanner: token too long`、exit 1 | acceptable（ラウド。メッセージは内部的） |
| s8 | argv と stdin の併用 | 併合される | kind |
| s9 | BOM 付き先頭行 | `\xef\xbb\xbfa/a.go` が warning | acceptable（BOM 除去の余地、微細） |
| s10 | 引数なし・stdin が TTY | **ブロックして待つ**（grep 型） | acceptable（Unix 慣習。README には「no args = stdin」とある） |

### 2.3 壊れたフラグ

| # | 操作 | 実際の挙動 | 評価 |
|---|---|---|---|
| f1 | `-format yaml` | `test-detect: unknown -format "yaml" (want pkg|space|dir|json)`、exit 2 | kind（有効値を列挙。ただし検査は render 時＝スキャン後） |
| f2 | `-frmat json`（タイポ） | `flag provided but not defined` + usage、exit 2 | kind |
| f3 | `-exclude '['` | `invalid value "[" for flag -exclude: error parsing regexp: missing closing ]`、exit 2 | kind |
| f4 | `-root /nonexistent` | `lstat ...: no such file or directory`、exit 1 | kind |
| f5 | `-root a/a.go`（通常ファイル） | `<file>: no go.mod found`、exit 1 | acceptable（「ディレクトリではない」と言う方が親切→F4） |
| f6 | `-root go.mod`（go.mod ファイル） | **半成功**: go.mod の dir がモジュールとして走査されるが、changed path の解決はファイルパス基準で全て warning、空出力 exit 0 | **misleading**（F4） |
| f7 | `-root ''` | cwd に解決され正常動作 | acceptable |
| f8 | `a/a.go -format space`（フラグを後置） | flag pkg が位置引数で止まり、`-format space` がファイル名として**沈黙で捨てられる**。pkg 形式で出力 | **silently wrong**（F3） |
| f9 | `-- a/a.go` | 正常 | kind |
| f10 | `-exclude '.*'` | 全 drop で空出力（-verbose なら理由が見える） | acceptable |
| f11 | `-h` | usage を出して **exit 2** | acceptable（慣習は exit 0、微細） |

### 2.4 壊れたスキャン対象ツリー

| # | 操作 | 実際の挙動 | 評価 |
|---|---|---|---|
| t1 | go.mod なし | `<root>: no go.mod found`、exit 1 | kind |
| t2 | go.mod に module 行なし | `<path>: no module directive`、exit 1 | kind |
| t3 | `module // c` のみ | 同上 | kind |
| t4 | `module "example.com/q"` | 正常 | kind |
| t5 | import 宣言が途中で切れた .go | warning + 復元した import は保持（テスト済みの設計） | kind |
| t6 | package 句なしの .go | `parse error (…:1:1: expected 'package', found 'func'); imports may be incomplete`、exit 0 | kind |
| t7 | 空の .go ファイル | 同上（found 'EOF'） | kind |
| t8 | ネストした go.mod | 別モジュールとして走査、クロスモジュール edge も張られる | kind |
| t9 | dir への symlink + 壊れた .go symlink + 自己参照 symlink | dir symlink は追わない（ループなし）。dead symlink は `parse error (open …: no such file)` で warning | acceptable（io エラーが "parse error" と名乗るのは微細な不整合） |
| t10 | chmod 000 の dir | `open …: permission denied`、exit 1 | kind（計算不能を隠さない） |
| t11 | **`a/go.mod/` というディレクトリ** | 親パッケージ `a` 全体が走査から**沈黙で消える**。`a/a.go` は `not in scanned graph` warning（理由は虚偽） | **misleading**（F6、nested module 判定が `os.Stat` の成功だけ見て IsDir を見ないバグ） |
| t12 | 無関係な subdir に壊れた go.mod（空） | 全スキャンが exit 1 で停止 | acceptable だが波及範囲が全滅的（F8：設計ノート） |
| t13 | `_test.go` という名の非テストファイル | hasTests に数えられる | kind（ファイル名がシグナル、設計どおり） |

### 2.5 環境・使い方のずれ

| # | 操作 | 実際の挙動 | 評価 |
|---|---|---|---|
| e1 | サブディレクトリで `git diff --name-only` を `-stdin` に流す | diff は repo 相対を吐くので、`-root` が repo なら正常 | kind |
| e2 | サブディレクトリで cwd 相対パスを渡す（`cd inspect; test-detect -root .. inspect.go`） | `Dir("inspect.go")="."` → **root パッケージを seed、警告なし** | **silently wrong**（F2） |
| e3 | `git status --porcelain`（`status.relativePaths` で cwd 相対＋マーカー） | s6 同様に warning 群 | acceptable |
| e4 | `-format json` で空結果 | **`null` を出力（末尾改行なし）**。pkg/space/dir は完全な空出力なのに対し json だけ `null` — README の「empty result means empty output」契約違反。`[ -n "$out" ]` で gate する CI は `null` を非空と見なす | **misleading**（F5） |
| e5 | `go run .` を違う dir で実行 | `no Go files in <dir>`（go ツール自身のエラー）、exit 1 | kind（ツールの管轄外） |

## 3. 目立つ発見

### F2（根干）: ルートパッケージ磁石 — basename に潰れた入力は静かに root を seed する

`resolveChanged` は `filepath.Join(root, input)` → `Dir` → `byDir` 引きの順で解決する。ディレクトリ成分を持たない、または潰れる入力は `Dir = root` となり、root にパッケージがある限り**必ずそこを seed する**。実リポジトリ（root に package minigo が存在）での再現:

```console
$ test-detect typo.go            # 存在しないファイル名1語
github.com/podhmo/minigo
github.com/podhmo/minigo/cmd/minigo
...                               # exit 0、警告なし

$ test-detect 'inspect\inspect.go'   # Windows 区切り（Linux では \ はファイル名の一部）
github.com/podhmo/minigo          # ↑と同じ誤答。本来の答は e2e_test/generator/scanx を含む
```

「削除されたファイルは dir 経由で解決する」という正当な機能（i2）と同じ経路なので、存在しない basename 入力を警告で弾くことは原理的に不可能。ただし**判別可能な部分集合**はある:

- 入力に `\` を含む（Unix 上ではほぼ確実に区切り文字の間違い）→ 警告で弾ける
- 入力が実在する**ディレクトリ**（`x.go` という名前の dir 含む）→ stat すれば分かる → F1 と同じ処置
- basename のみかつ `Dir == root` の入力は「root 直下のファイルのつもりか」に他ならず、正当ケースでもある → 警告すると root 直下の削除ファイルが誤爆する。**残る設計上の緊張**として README に書くのが現実的

### F1: ディレクトリ入力と `./...` が完全に沈黙する

```console
$ test-detect inspect       # ディレクトリを指定 → パッケージを聞いたつもり
                            # 空出力、exit 0、警告すらなし
$ test-detect ./...         # go のパターンを誤って渡す
                            # 同上
```

`docs/README.md` の黙殺は diff 混入を見据えた設計として妥当だが、ディレクトリは「パッケージを指定した」という強い主張であり、空出力は「影響なし」の嘘になる。stat して IsDir なら `is a directory (skipped)` と警告する、または dir 以下を展開するかは設計判断 — 最小の修正は警告。

### F3: 位置引数の後に置いたフラグがファイル名として飲まれる

```console
$ test-detect a/a.go -format space
example.com/m/a           # pkg 形式で出る。-format と space は「.go でない入力」として沈黙で捨てられる
```

Go の flag パッケージの仕様だが、このツールの「非 .go は黙殺」ルールと噛み合って二重に沈黙する。`fs.Args()` に `-` 始まりの要素があればエラーにするのが安い（`-` で始まる正当なファイル名は `--` が必要、という既存慣習とも整合）。

### F4: `-root` がファイルだと半成功になる

`-root a/a.go` → `<file>: no go.mod found` exit 1（まあ良い）。しかし `-root go.mod` は WalkDir が「ファイル」を root として入るため go.mod を発見し、その dir をモジュールとして走査してしまう。changed path の解決は `Join(file, input)` になるので全て warning で空出力 exit 0 — **走査は成功・回答は空**の半端な状態。開始時に「root はディレクトリか」を検査すべき。

### F5: `-format json` の空結果が `null`

pkg/space/dir は空集合に対してバイトゼロを返す契約（README 明記）だが、json は `nil` スライスの marshal で `null`（改行なし）を返す。CI で `[ -n "$out" ]` する利用者には「非空=テストあり」と読める。`[]\n` にするのが正直。

### F6: `a/go.mod/` という名のディレクトリでパッケージが消える

```go
// scan.go の nested module 判定
if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
    return fs.SkipDir   // a/go.mod が「ディレクトリ」でもスキップされる
}
```

ディレクトリ `a/go.mod/` が存在すると `os.Stat` は成功するので `a` 全体が nested module 扱いで走査対象から外れる。`a/a.go` を渡すと `not in scanned graph` — 警告は出るが**理由が違う**（スキャン外れたのはツールのバグ）。`Stat` の結果に `!info.IsDir()` の条件が要る。被害は小さいが本物のバグ。

### F7: `not in scanned graph` が原因を潰す

`-root` の外、削除された dir、`testdata`/`vendor`/`_`/`.*` でスキップされた dir、`.go` が無い dir — 4 つの異なる原因が 1 つのメッセージに潰れる。`where`（入力パス）はあるが `why` が無いので、ユーザは「パスが間違っているのか、ツールが無視する dir なのか」を自分で調べ直す必要がある。`abs` が `absRoot` の外か、dir が存在しないか、を見れば大部分は区別できる。

### F8（設計ノート）: 壊れた go.mod 1 個で全滅

無関係なサブディレクトリの空 go.mod でも全スキャンが exit 1。「成功のふりをしない」には忠実だが、波及が全滅的なので「警告して当該サブツリーだけスキップ（= その中身の edge を諦める、と明言する）」という部分成功もあり得る。現状維持か degrade するかは設計判断として記録に留める — 現状は少なくとも嘘をついていない。

## 4. 分類

### ツール側の問題（直す対象）

| # | 発見 | 症状 | 修正方針 |
|---|---|---|---|
| F1 | ディレクトリ / `./...` 入力の沈黙 | silently wrong | `resolveChanged` で stat し、IsDir なら `is a directory (skipped)` と警告 |
| F2 | ルートパッケージ磁石（`\` パス、cwd 相対、`x.go` dir） | silently wrong | `\` を含む入力を警告; F1 の IsDir 警告が dir ケースを覆う。basename-only の残りは設計限界として README に明記 |
| F3 | 位置引数後のフラグが黙殺 | silently wrong | `fs.Args()` 中の `-` 始まり要素をエラーに（exit 2） |
| F4 | `-root` がファイル | misleading | 開始時に `os.Stat(root)` が dir か検査し、ファイルならエラー |
| F5 | `-format json` 空結果が `null` | misleading | 空集合を `[]` に（末尾 `\n` も揃える） |
| F6 | `a/go.mod/` dir で nested module 誤判定 | misleading | `os.Stat` 結果の `IsDir()` を確認する条件を追加 |
| F7 | `not in scanned graph` が why を潰す | acceptable→改善 | outside-root / dir 不存在 / 走査対象外の区別を警告文に乗せる |
| F8 | 壊れた go.mod で全滅 | acceptable（設計判断） | レポートに留める。loud を維持するか warn+skip するかは要相談 |

その他の微細な観察（要対応というより記録）: `-h` が exit 2、stdin TTY でブロック（grep 型の慣習）、stdin の `bufio.Scanner: token too long` はラウドだがメッセージが内部名、`io` 由来の失敗が "parse error" と名乗る、porcelain 形式へのヒントが無い。

### minigo インタプリタ側の問題

**構造上ゼロ — 想定どおり。** このツールはインタプリタを呼ばない（`parser.ImportsOnly` のみ）。インタプリタ由来で説明できる症状は今回のプローブでは 1 件も観測されなかった。

隣接する要望として: スクリプト版/REPL 版の検出器が必要とする「ルート配下の全パッケージ列挙（ネストした go.mod 横断・テストファイル有無付き）」の欠落は、スパイク時点で既に TODO.md に起票済み（"Repo-enumeration surface for scripted detection"、"REPL-side package introspection"）。今回のエラー UX 観測から新たに追加されるインタプリタ側の課題はない。

## 5. まとめ

- プローブ約 70 件。silently wrong 4 系統（F1-F3 と F2 のディレクトリ経路）、misleading 2 件（F4, F5）、バグ 1 件（F6）、改善余地 1 件（F7）。それ以外は kind/acceptable で、スキャン側のラウド失敗は設計どおり機能している。
- このツールの「正しい答」は dir 粒度なので、ファイル名のタイポは吸収される（i3）— 寛容さと嘘の紙一重で、境界は「入力がファイル名まで正しいか」ではなく「**入力のディレクトリ成分が意図どおりか**」にある。F1-F3 はすべて「入力の主張を黙って別の意味に読み替えた」ケースであり、ユーザの引用した基準の「成功したふりをしない」に直撃する。
- F1-F7 はこの実験の続きとして、1 発見 = 1 PR の修正スタックで対応する。
