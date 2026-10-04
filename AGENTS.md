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

`TODO.md` is an index only — task entries live in the topic files under `docs/todo/` (one file per topic). Record remaining tasks there; also record a task when you find a bug unrelated to your current work.

- Add open tasks to the `## Open` section of the matching `docs/todo/<topic>.md` as `- [ ]`, or `- [-]` when partially done. If no topic fits, create a new topic file and add a row to the index table in `TODO.md`.
- Keep open entries compact: a bold title, 1–3 sentences of what remains, and links to `docs/sketch/` reports or issues/PRs for detail. Do not write root-cause or fix narratives here — those belong in `docs/sketch/`.
- When a task is done, move its entry to the same file's `## Done` section and mark it `[x]`. Done entries may keep their full text as history; do not grow them after the move.
- Update the topic's row in `TODO.md` whenever its open count changes or a topic file is added/removed. Never add task entries to `TODO.md` itself.

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

