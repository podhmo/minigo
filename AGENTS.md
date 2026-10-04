# 必ずやること

コードを変更したら以下を必ずやってください

コードのフォーマット（goimportsを使ってるのでunused importも消えます）

```shell
make format
```

静的解析

```shell
make lint
```

全体のテスト

```shell
make test
```

## 補足 個別のテスト

- トップレベルのテストは`go test ./...`
- examples/の方のテストは`go -C ./examples/name test ./...`

で個別に実行できます。

# コミュニケーション

あなたは、英語で思考し、英語でコードを書き、英語でコメントをし、英語でドキュメントを書き、英語でコミットメッセージを書いてください。基本的にはすべての施行を英語でやってください。

ただしユーザーの入力への返答は日本語で行ってください。

*Makefile内の `go run` コマンドについて**:
- Makefile で `go run` を使用してローカルの Go プログラムを実行する場合、原則として `go run ./ <引数...>` の形式を使用してください。これは、カレントディレクトリの main パッケージを実行することを明示し、特定のファイル名 (`main.go` など) への依存を避けるためです。

# TODO.md

Record remaining tasks in TODO.md. If you find a bug unrelated to your task, add it there too.

- TODO.md tracks actionable work only: `[ ]` = open, `[-]` = partially done. Do not leave `[x]` entries in TODO.md.
- Keep each entry short: a bold title, 1-3 sentences of what remains (not what was done), and links to `docs/sketch/*` reports, issues, or PRs. Status narratives and fix logs do not belong in TODO.md.
- When an item completes, move its text to TODO-archive.md verbatim (append at the bottom of the matching section; create a `###` group if none fits). The archive is append-mostly history — entries there are never rewritten.
- When a `[-]` item's sub-items complete, move those sub-items to TODO-archive.md under the parent's name; the `[-]` parent stays in TODO.md until it is fully done.
- If a new task needs more than a few lines of context, write the detail in `docs/sketch/` and link to it.

# 制約事項

- 禁止。`go/packages`や`go/types` はimportがeagerになってしまうので使わないこと。
- 禁止。 `go list` の利用も禁止します。
- 禁止。go buildで作成したバイナリはコミットしないこと。
- 禁止。デバッグ用に一時的に作成したファイルはコミットしないこと。
- 禁止。 github.com/stretchr/testify は使わないこと
- 強制。 テストの比較時には github.com/google/go-cmp/cmp を使うこと。
- 強制。ログにはlog/slogを使うこと。使うときはcontextを受け取るメソッドを使うこと（e.g. DebugContext()）
- 禁止。logパッケージを使わないこと。
- 強制。docs/*.mdは英語で書くこと
- 強制。docs/sketch/*.mdは英語で書くこと
- 強制。docs/sketch/ja/*.mdは日本語で書くこと
- 強制。コミットメッセージは英語で書くこと
- 禁止。GOPATHを変更しないでください。特にテストでos.Setenvなどするのは禁止します。

# 補足情報

