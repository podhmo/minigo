# 所有権とライフタイム（D-own の要約）

[../TODO.md](../TODO.md) の M0-4、M0-5、M1-1、M3-1、M5-1、M5-2 から参照する。原典は [discussion #1090 のコメント 19667469](https://github.com/podhmo/minigo/discussions/1090#discussioncomment-19667469)。ここに書いたのは、計画書の §2.6 を補うための作業用の要約である。

## 所有表

| 所有者 | 所有するもの | ライフタイム | 共有 |
|---|---|---|---|
| Engine のソース管理 | resolver、ビルドコンテキスト、FileSet、ソース版、AST と index のキャッシュ | Engine と同じ | 同じ条件・同じソース版なら Session 間で共有 |
| PackageImage | 宣言、AST、ファイルごとの import spec、宣言位置、言語バージョン | 公開後は不変 | 共有 |
| Session | PackageInstance の表、実行時の型と関数、Session 依存のキャッシュ | 複数の呼び出しにまたがる | 共有しない。他の Session に渡さない |
| PackageInstance | globals、初期化状態、実行時の ImportRef、materialize した値 | 所有する Session と同じ | 所有する Session の goroutine だけが使う |
| Process | goroutine のライフタイム、待機、終了、外部コールバックの所有 | 通常の Call 1 回で 1 つ開き、戻るときに goroutine を終える | 同じ実行の VM 間で共有 |
| VM | 実行中の frame、panic の伝播 | 1 つの実行主体。他の goroutine からの再入は子 VM へ | 共有しない |
| Frame | ローカル、upvalue の参照、命令位置、defer | 呼び出しと同じ。escape した Cell は別に生き続ける | 共有しない |

## PackageImage に入れてはいけないもの

初期化の結果、グローバルの Cell、実行可能な `Function`、materialize された `TypeDef`、host を捕捉するクロージャ。

`ImportRef` も入れてはいけない。`sync.Once` で解決結果を保持するので、Session ごとに結果が変わり得るからである。import spec（テキスト）は共有してよい。

## 問い合わせのポリシーと結果

- `IndexedOnly`: すでにある index にだけ答える。新たな index、初期化、実行は要求しない。
- `AllowIndex`: locate、parse、index までは要求してよい。初期化と、スクリプト・host の実行はしない。
- 結果は Found、Missing（その範囲に宣言がない）、Deferred（まだ答えがない。不正な Go とは扱わない）、Failed（原因を保持して返す）の 4 種類。nil で「存在しない」を表してはいけない。

## pin すべき振る舞いの例（M0-4、M0-5 の題材）

D-own §14 の例を使う。`script/main.go` は `dep` を import し、`Main()` が `dep.Next()` を返す。`dep` はパッケージ変数 `n = 10` を持ち、`Next()` は `n` を増やして返す。

1. Engine はソースを取得して PackageImage を作るが、`Main` は実行しない。
2. `Main` の最初の呼び出しでコンパイルが走る。`dep.Next` の宣言情報が要るので、`AllowIndex` で dep を index する。このとき `n` は初期化されない。
3. 実行が `dep.Next` に到達した時点で、Session A の dep インスタンスが初期化される。結果は 11。
4. Session A でもう一度呼ぶと 12 になる。新しい Process でも Session の状態はリセットされない。
5. Session B では別のインスタンスなので 11 になる。AST と index は共有されている。
6. dep を編集したあとの新しい Session C は新しい版を使う。Session A のインスタンスはそのままにする。
7. Session A の dep インスタンスを Session B の Call に渡すのは誤用で、所有権チェック（M3-1）が捕捉する。

## 計測値（D-own のスパイク）

12 ファイル × 20 関数のパッケージをリロードして呼び出すと、約 2〜3 ms かかった。AST と index の完全な JSON ハッシュで監査すると約 1〜1.6 s で、同じ AST を何度も辿るのが主なコストだった。そのため資産監査はオプトインとし（M3-2）、軽量な所有権チェックと分ける。
