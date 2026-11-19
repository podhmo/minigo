# 可変グローバルの棚卸し（M1 / K10）

計画の K10 は可変グローバルの監査を要求する。生産コード（`*_test.go`、`examples/`、`testdata/`、`tools/`、`.claude/` 以外の全 Go ファイル）のパッケージ変数を棚卸しした結果 98 件。大半は init 以降書き込まれない不変テーブル（opName 表・種別表・予約語リスト等）で、ここでは**実際に書き込まれるもの**を分類する。生産コードで Session ごとの可変状態を持つパッケージ変数は見つからず、Session への移設対象はない（`templateFuncMap` は P2-12 で Session フィールド化済み）。

## 計測用

- `internal/spike` の各カウンタ（`slotsPromoted` 等、`atomic.Int64`、spike ビルドタグ時のみ実体化）— P4 の静的判定を動的観測で裏付けるための計測
- `asmimpl.go` の `fipsIndicator`（`atomic.Uint32`）— FIPS 動作フラグ、asm 実装が自身の状態を記す計測値
- `runtime.methodSetEpoch`（`atomic.Uint64`）— メソッド集合の更新を `MethodSetsChanged()` で単調加算。`selectSlot` 系の `(TypeID, epoch)` ガードが参照する**全 Session 共通の有効性シグナル**で、単調性が正しさの根拠のため Session 単位には分けない
- `runtime.keyNonce`（`atomic.Int64`）— opaque キーの単調 nonce。一意性だけが要件なので共有で問題ない

## 明示的なプロセス共有

- `internal/hostbridge/valueof.go`: `hostNilPtrTypes` / `hostNils` / `structOrigs`（`sync.Map`）— 不変な host ミラーのキャッシュ。Session 横断で共有するのが目的
- `runtime/arraylen.go`: `sharedArrayFolds`（`sync.Map`、weak key）— `ast.ArrayType` 単位の fold 結果。値は不変、weak キーで解放される
- `runtime/value.go`: `opaqueKeyOrigs`（`sync.Map`）— opaque キー → 起源 Value。キーが nonce で一意なので Session 間衝突は起きない
- `vm/frame.go` `framePool` / `vm/iter.go` `iterPool`（`sync.Pool`）— オブジェクト再利用のプロセス共有
- `intrinsics_sync_atomic.go` `atomicMu`（`sync.Mutex`）— atomic 組込みの直列化。共有が仕様
- `vm/ffi_marshal.go` `marshalHost`（`*hostbridge.Marshal`）— `init()` で一度だけ代入される hook コンテナ（var 初期化の依存閉路を断つため init 内代入。以後読み取り専用）
- `pkg/locator` `getGoRootOnce` — GOROOT 環境の遅延メモ化。プロセス環境の事実なので共有でよい

## テスト用ノブ

- `compile/typehint.go` `DeferFacts` — テストからだけ書き換える defer-fact 強制フラグ（`defer` 復元付き）。安価に difffuzz pin を回すための意図的なグローバル

## 方針

- これらはすべて根拠があるので残置。新しい可変パッケージ変数を足す変更はこの一覧への追記理由をレビューで問う。
- CI への組み込みは現時点では行わない — 検査対象が「代入されるパッケージ変数」であり、parser レベルの検査ではストア検出の誤検出（スライス要素・map キー・フィールド経由）が多い。一覧の人間側メンテナンスで十分小さい。
