# Quint メモ — lazy 初期化のモデル検査で調べたことと pitfalls

対象: [Quint](https://github.com/informalsystems/quint) 0.34.0。minigo の lazy package 初期化の状態機械をモデル化して検証するために使った（仕様は `spec/lazypkg.qnt`、分析は `docs/lazy-lifecycle-model.md`）。

## Quint とは何か

TLA+ 直系の形式仕様言語。Informal Systems 製。TLA+ の数学的記法（`\A`, `\E`, 箱・ダイヤの時間演算子）を、プログラミング言語風の構文に置き換えたもの。内部で TLA+ に変換され、検査は TLA+ エコシステムのモデルチェッカ（Apalache / TLC）が担う。

「qunit」と聞くと JS のユニットテストフレームワーク QUnit と混同しがちだが、まったくの別物。位置づけとしては**テストではなく仕様の探索**に使う道具で、Go のコードそのものではなく設計（状態遷移・並行性のプロトコル）を検証する。

## ツールチェインの構成

```
quint parse/typecheck  … 構文・型のチェック（速い、まずここで回す）
quint run              … ランダムシミュレーション。不変条件違反を「速く粗く」探す
quint verify           … Apalache 経由の記号モデル検査。有界の全探索
quint repl             … 対話環境
```

エコシステムの依存はこうなっている:

- quint 本体は npm パッケージ（`@informalsystems/quint`）。Node があれば入る
- `quint run` のデフォルトバックエンドは **Rust 製 evaluator を初回実行時に GitHub Releases から取ってくる**
- `quint verify` は **Apalache のディストリビューションを初回にダウンロード**し、Java で動かす。最近の Apalache は **Java 21 以上を要求**する

## pitfalls（実際に踏んだ順）

### 1. Rust evaluator の取得が GitHub rate limit で死ぬ

`quint run` の初回に `Release v0.8.0 not found: Error: Failed to fetch from GitHub: rate limit exceeded` と出てシミュレーションが一切動かなかった。CI や共有 IP 環境だとほぼ踏む。

**回避**: `--backend=typescript` を付ける。TypeScript 実装にフォールバックする（速度は落ちるが数百 traces/sec は出る）。

### 2. `--invariant` は1回しか指定できない

複数書くと `--invariant can not be specified more than once`。全部見たいときは仕様側で `val allInvariants = inv1 and inv2 and ...` を定義してそれを指定する。

### 3. 集合とリストで演算子が違う

これが一番ハマった。Quint は型付きで、`in` / `contains` は**集合専用**。リストの membership は書けない（コンパイルエラー: `Couldn't unify set and list`）。リストの要素検索は自前で:

```
pure def inList(l, x) = l.indices().exists(i => l.nth(i) == x)
```

重複がないことのチェックは、集合に潰してサイズ比較するのが手軽:

```
l.foldl(Set(), (acc, x) => acc.union(Set(x))).size() == l.length()
```

### 4. Map リテラルより関数で書く

`Map(k -> v, ...)` の構文は版によって怪しい。キーが少ないなら `pure def deps(p) = if (p == "a") ... else ...` の関数で書くほうが確実。初期状態の一様マップは `pkgs.mapBy(p => initial)` が便利。

### 5. 状態を更新しないアクションはデッドロック判定を汚す

`quint run` は「どのアクションも有効でない状態」をデッドロックとして報告する。初期化が正常に終わった終端状態までデッドロック扱いされてしまうので、**終端条件つきの stutter アクション**（全変数を自分自身に代入する `settle`）を入れておくと、真のデッドロック（スタックが残ったまま動けない等）だけが報告される。

ただしこの stutter は `quint verify` では状態空間を膨らませる。verify が重いときは stutter を外してdepth bounded で回すのがよい。

### 6. `quint verify` は Java のバージョンでコケる

手元の Java 17 では `UnsupportedClassVersionError (class file version 65.0)`。Apalache 0.62 系は Java 21 必須。`--apalache-version=0.44.11` で旧版に下げる手もあるが、quint 本体とのプロトコル不整合（`Unknown key`）で結局動かなかった。**素直に openjdk-21 を入れて `JAVA_HOME` と `PATH` を差し替えて実行する**のが正しい。

```
JAVA_HOME=/usr/lib/jvm/java-21-openjdk-amd64 \
PATH=/usr/lib/jvm/java-21-openjdk-amd64/bin:$PATH \
quint verify spec/lazypkg.qnt --invariant=allInvariants
```

### 7. verify は指数的に重い

3パッケージ・リスト長数個程度の小さなモデルでも、深さ8〜9で1ステップ数十秒かかる。`run`（ランダム）で粗く潰してから `verify`（記号）に入るのが正しい順序。witness（「この振る舞いは起きうる」ことの確認）には、**成立してほしくない性質を invariant にして違反トレースを取る**逆転の発想が便利（例: `partialServes == 0` を不変条件として指定し、違反＝循環 init の部分読み出しが実際に起きることの証拠にする）。

### 8. `head()` はリストの先頭

スタックを `List` でモデル化するとき、`append` すると末尾がトップになるが `tail()` は先頭を落とすので噛み合わない。**先頭をトップに揃える**（push は `List(p).concat(stack)`、pop は `tail()`）ほうが `head()`/`tail()` と整合して素直。

## 設計をモデル化するときの指針（今回の経験から）

- 現実のコードをなぞりすぎない。検証したい問い（今回なら「循環 init でデッドロックしないか」「bootstrap は高々1回か」）に効く部分だけを抽出する。goroutine 間の sync.Once ブロッキングのような「機械的に自明な並行性」は捨てて、再入するサイクル（本質的に難しい部分）だけをモデルに入れた
- 不変条件は**状態述語**しか書けないので、遷移の性質（単調性など）はゴースト変数（high-water mark）を足して状態にエンコードする
- 失敗の伝播（dep が failed なら importer も failed）は非決定性で書けるが、モデルを小さく保つためにどこまで入れるかはトレードオフ

## 参考

- 公式チュートリアル: <https://quint-lang.org/docs/getting-started> — 構文の揺れ（版ごとの差）があるので、**使うバージョンの docs を見ること**
- 実例集: <https://github.com/informalsystems/quint/tree/main/examples>
- TLA+ 側の知識は *Specifying Systems*（Lamport）が今でも基礎になるが、PlusCal/TLA+ を直接書く必要はなかった
