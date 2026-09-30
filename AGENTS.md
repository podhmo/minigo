# 必ずやること

コードを変更したら以下を必ずやってください

コードのフォーマット（goimportsを使ってるのでunused importも消えます）

```shell
make format
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
- この形式の採用経緯や詳細については、`sketch/trouble.md` の `go run` に関するセクションを参照してください(注: go-scan 側のファイル。移行元のリポジトリ参照)。
- Makefile内の `go run` コマンドを編集する際は、安易に他の形式（例: `go run main.go ...`）に変更せず、上記の標準形式とその理由を理解した上で慎重に行ってください。

# TODO.md

- 未実装のタスクと実装済みのタスクをチェックボックスで管理してください
- 機能全体が完了した場合にはimplementedのセクションに移動します

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
- 強制。sketch/*.mdは英語で書くこと
- 強制。sketch/ja/*.mdは日本語で書くこと
- 強制。コミットメッセージは英語で書くこと
- 禁止。GOPATHを変更しないでください。特にテストでos.Setenvなどするのは禁止します。

# 補足情報

- このリポジトリは github.com/podhmo/go-scan の `minigo2/` を `minigo` として独立させたものです。go-scan とは github.com/podhmo/go-scan のことで、トップレベルのパッケージそのものを指します。
- 依存関係: go-scan へのモジュール依存はありません。locator だけ `pkg/locator` に vendoring しています(出所は `pkg/SOURCE.md` 参照)。`examples/convert-define` も同様に、使っている go-scan のコードと `examples/convert` のライブラリを `examples/convert-define/pkg/` に vendoring しています(出所は `examples/convert-define/pkg/SOURCE.md` 参照)。
