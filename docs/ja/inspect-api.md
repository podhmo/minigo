# inspect パッケージ 使い分けガイド

`import "minigo.dev/inspect"` はスクリプト向けの静的解析 API。`inspect/stub.go` に宣言があり、`inspect.go`（ルート）が intrinsic としてバインドしている。stub の本体は実行されない（`panic("minigo intrinsic")`）—— gofmt/gopls を効かせるための宣言だけで、実体はホスト側の intrinsic 実装。

このドキュメントは「何を調べたいか」から関数を引くための使い分け表と、名前が似ている関数の違い、罠とその逃げ方をまとめたもの。各関数の正確な契約は `inspect/stub.go` の doc コメントが正本。

## まず押さえる概念

- **ロード経路が2系統ある**: 「バインド済み（bound）」のホストパッケージと「ソースから index した」パッケージは**別物**。bound パッケージは index を持たないので `Decls` は Kind=`host` の擬似 decl を合成し、`AST`/`Body`/`Pos` は trap する。`Standard()` は bound なら登録時の宣言、ソースなら GOROOT 配下かどうか。
- **trap 系と Lookup 系の対**: `Symbol`/`DirOf`/`PackageOf`/`SourceOf`/`FileOf` は見つからないと trap（ホストプロセスを巻き込まない範囲でスクリプト実行が死ぬ）。`LookupSymbol`/`LookupDirOf`/`LookupPackageOf`/`LookupSourceOf`/`LookupFileOf` は nil を返す。「見つからない」以外の失敗（見つかったが壊れている、ポリシー拒否）は Lookup 系でも error を返すのが設計上の境目。
- **Decl と TypeExpr は別物**: `Decl` は宣言そのもの（名前・種類・doc・位置）、`TypeExpr` は型のスペリング（`[]map[string]T` や `*T` という式）。`Decl` から `TypeExpr` へは `Def`/`DeclType`/`Signature`/`Initializer` で降りる。`TypeExpr` から `Decl` へは `SymbolID`/`Resolve` で上がる。

## ロード系（パッケージを得る）

| 関数 | 何を返すか | miss の挙動 | 使う場面 |
|---|---|---|---|
| `PackageOf(path)` | import path の正規パッケージ。**bound 登録があればそれ優先**、なければ lenient ビルドのソース | trap | 通常はこれ。bound の有無を意識しないで済む |
| `LookupPackageOf(path)` | 同上 | **nil**（path がどのモジュールにも解決しないとき）| `Main` の入口解決、graceful な失敗を返したいとき |
| `BoundOf(path)` | bound 登録されたホストパッケージだけ。ソースには落ちない | **nil**（bound でないだけ）| 「この path は bound か？」の一発判定。bound 由来の制限（index なし等）を調べるとき |
| `SourceOf(path)` | **ソースだけ**。bound シャドウを迂回して実ソースを index | trap（path 不存在） | bound パッケージが index を持たなくて詰まったとき。`-stdlib walk` の実体 |
| `LookupSourceOf(path)` | 同上 | **nil**（path 不存在・bound だが背後にソースのない合成 path） | bound かどうか不確かな path から graceful にソースを試す入口（extract-schema `-pkg` の retry で使用） |
| `DirOf(dir)` | ディレクトリのソースパッケージ（lenient） | trap | `./target` のようなローカル dir 指定 |
| `LookupDirOf(dir)` | 同上 | **nil**（dir 不存在） | 存在確認付きの入口 |
| `FileOf(file)` | 単一ファイルを1ファイルパッケージとして | trap | ファイル単位のプローブ |
| `LookupFileOf(file)` | 同上 | **nil**（file 不存在） | 存在確認付きの単ファイルプローブ |
| `Packages(root)` | root 配下の全パッケージ dir（入れ子 go.mod 越しに最寄りのモジュールが claim）を `*RepoPackage` の列として | error（root 不存在・壊れた go.mod）。Lookup 系なし | affected-package 検出など「リポジトリ内の全パッケージ」が要るとき。列挙だけで parse はしない——decl/import は各 `Dir`/`TestFiles` に `DirOf`/`FileOf` を当てる |
| `Current()` | 呼び出し側スクリプト自身のパッケージ | — | 自分の decl を introspect するとき |

### よくある誤配線

- **`PackageOf("time")` で stdlib のソース decl が取れない** — bound 登録があると index なしの bound オブジェクトが返る（`Decls` は `Kind "host"` の擬似 decl）。`SourceOf("time")` で実ソースが取れる。`-pkg` でスキーマ生成したいのに `type "X" not found` になる典型例（corpus m05）—— extract-schema は `BoundOf(target) != nil` のとき `LookupSourceOf(target)` に retry する形で解消済み（PR #900）。
- **`DirOf` に import path を渡す** — `-pkg` 引数はディレクトリではないので trap。`PackageOf`/`LookupPackageOf` を使う。
- **`DirOf` したモジュール外パッケージの `sid.PackagePath` が `<dir>/<abs>` という疑似パスになる** — `PackageOf` でそれを追跡すると `resolve` error で trap。`LookupPackageOf`→nil→leaf に落とすのが extract-schema の取った逃げ道（PR #881）。
- **bound かどうかを `Standard()` で判定しようとする** — `Standard()` は GOROOT 判定であって bound 判定ではない。bound 判定は `BoundOf(path) != nil`。

## 宣言（Decl）を得る・調べる

| 関数 | 対象 | 備考 |
|---|---|---|
| `Decls(pkg or file)` | **トップレベル宣言のみ**（func/type/var/const） | **method は含まれない**（receiver に属するため）。見落としやすいので注意 |
| `Symbol(pkg, name)` | トップレベル名で1件 | miss で trap |
| `LookupSymbol(pkg, name)` | 同上 | miss で nil。「任意の plugin 入口関数を探す」はこれ |
| `Methods(x)` | `x` が type decl ならその型のメソッド、pkg/file ならそこに宣言されたメソッド全部 | `Decls` が拾わない部分 |
| `MethodSet(typeDecl)` | `*T` 経由で使えるメソッド集合（promoted 含む、name ソート） | interface 実装の確認、`MarshalJSON` を持つかの判定など |
| `Implementers(pkg, ifaceDecl)` | `pkg` 内で `iface` の要件を満たす型 decl | iface は interface 型でないと trap |
| `Kind(decl)` | `"func"/"method"/"var"/"const"/"type"/"host"` | 分岐の最初に使う |
| `Doc(decl)` | doc コメント（末尾改行は除去済み） | `Field.Doc`/`File.Doc` も同じ契約 |
| `Pos(decl)` | `*Position`（File/Line/Column の構造体） | host 擬似 decl は nil |
| `Signature(funcDecl)` | `*Sig`（Recv/Params/Results） | Params は**書かれたフィールドグループ単位** — `a, b int` は1要素。個数は `NumParams()`/`NumResults()` で |
| `TypeParams(decl)` | 型パラメータの Field 一覧 | generic decl 用 |
| `Def(typeDecl)` | 宣言された `TypeExpr` | `type T struct{...}` の右辺 |
| `DeclType(varOrConstDecl)` | 注釈された型式（空 spec は継承型を返す） | untyped spec は nil |
| `IsAlias(decl)` | `type X = int` かどうか | alias は target を指すので追跡先が変わる |
| `Initializer(decl)` | 初期化式（`TypeExpr` として） | 式の中身を見たいとき |
| `Value(pkg, name)` | メンバの実値。const は**materialized scalar**（untyped→Go 値、named basic→underlying scalar）、var は書き込み可能 cell（`*v` で読む） | 構造体型 const は scalar でないため runtime 値のまま |
| `TypeOf(decl)` | `*runtime.TypeDef` | 型同一性の比較用 |
| `AST(x)` / `Body(funcDecl)` | 生 AST / 関数本体 `Node` | 生 AST 経由で host reflect facade に落とす逃げ道。host 擬似 decl では trap |
| `Nodes(node)` | 子 Node 一覧（Role つき） | Body 以下の走査 |
| `Expr(node)` | 式ノードを `TypeExpr` 化（宣言ファイルの名前解決つき） | |

### Decl 系の典型パターン

- **「全型を列挙」**: `Decls` → `Kind(d) == "type"` でフィルタ。**method を忘れない** — メソッド宣言の doc やシグネチャが必要なら `Methods(pkg)` も回す。
- **「この型は MarshalJSON を持つか」**: `MethodSet(typeDecl)` を name 一致で探す。value/pointer receiver の区別は MethodSet が「`*T` 経由」で正規化してくれる（extract-schema の `[]Byte` 判定で使用、PR #891）。
- **「enum かどうか」**: `EnumMembers(typeDecl)` が非空かどうか。const scalar に materialize されるメンバのみ数えられる（`const P Point = Point{...}` のような composite const は除外される——gc と同じ感覚）。
- **「arity チェック」**: `len(Signature(f).Params)` ではなく `NumParams()`。`func(a, b *C)` は ParamFields で1グループなので `len` だと通ってしまう（corpus p05 の教訓）。

## TypeExpr をたどる

| 関数 | 戻り値 | 使い分け |
|---|---|---|
| `TypeFields(te)` | composite（struct/interface）の member Field | **non-composite で trap**。Kind で先に絞る |
| `Children(te)` | 複合型の構成要素（`[]T`→`T`、`map[K]V`→`K`,`V`、`F[A]`→`F`,`A`） | 再帰 walk の基本 |
| `UnWrap(te)` | declared 型を1層剥がす | named 型の underlying に1歩降りる |
| `UnRef(te)` | pointer を1層剥がす | `*T`→`T` |
| `Origin(te)` | pointer・型遷移を全部追い切った基底式 | 「最終的にどの型か」一発 |
| `SymbolID(te)` | `*SymbolID`（`{PackagePath, Name}`）または nil | 名前を持たない式（composite、instantiation 全体）は nil |
| `Resolve(te)` | `*Decl`（宣言ビュー） | TypeExpr→Decl 方向の橋。bound パッケージの型（`time.Time` 等）は `Symbol` と同じ Kind=`host` 擬似 decl を返す（#921）。存在しない名前は trap |
| `SameType(a, b)` | 構造的等価 | タグや名前解決込みの厳密比較 |

### SymbolID の疑似パス

`SymbolID` が返す `PackagePath` には実在しない疑似パスがある。`inspect.NoDeclPath(path)` でまとめて判定できる。

| 値 | 意味 | 対応 |
|---|---|---|
| `":builtin:"` | predeclared 型・値（`int`/`byte`/`any`/nil など） | `BuiltinPackagePath` 定数で判定。decl に追跡しない |
| `":typeparam:"` | ジェネリック型パラメータ（`Box[T]` の `T`） | `TypeParamPackagePath` で判定。decl 追跡はしない（スキーマ walk では葉扱いに） |
| `"<dir>/<abs>"` | モジュール外を dir-load した疑似パス | `LookupPackageOf` が nil を返すので degrade 判断に |

### ハマりどころ（TypeExpr）

- **`SymbolID` が nil** — composite 型（`[]T` など）か instantiation（`Pair[int]`）。`Children` の先頭に base があるので再帰する。
- **`TypeFields` が trap** — struct/interface 以外に呼んだとき。`te.Kind`（`StructType`/`InterfaceType`）でガードするのが推奨の前処理。これは intrinsic/host 両側で同じエラー（#872 で契約 pin）。
- **`interface{}`/`any` のスペリング揺れ** — arg 境界では正規化済み（`anonExprOf`、#878）だが、`TypeExpr.Text` を文字列比較する自前コードは書かない。`SameType` か `SymbolID` 比較を使う。
- **named byte 型の chase** — `isByteSlice` のように「underlying が `byte` か」を知りたい場合、`SymbolID` → `LookupPackageOf`+`LookupSymbol`+`Def` で decl を辿る bounded loop（extract-schema の実装参照）。

## 名前が似ている関数のまとめ

- **`Symbol`/`LookupSymbol`/`SymbolOf`/`SymbolID`/`SymbolIDOf`** — `Symbol` は「パッケージ内を名前で検索（trap）」、`LookupSymbol` はその nil 版。`SymbolOf(v)` は「値の持ち主の decl」、`SymbolIDOf(v)` はその `{path,name}` 版（`SymbolID(te)` は TypeExpr 用で別物）。
- **`DirOf`/`LookupDirOf`/`FileOf`/`LookupFileOf`/`PackageOf`/`LookupPackageOf`/`BoundOf`/`SourceOf`/`LookupSourceOf`** — ロード経路の表（上）を参照。`*Lookup*` = nil 版、`Bound`/`Source` = 片側限定版。
- **`Fields`/`TypeFields`/`MReqs`/`IEmbeds`** — `Fields(decl)` は「**decl が指す型**の member」（struct の field、interface の member）。`TypeFields(te)` は「**この TypeExpr** が composite なら member」。`MReqs` は interface の named method spec だけ、`IEmbeds` は embedded element だけ。
- **`Methods`/`MethodSet`** — `Methods` は宣言ベース（source position 順、receiver-attached decl）、`MethodSet` は意味論ベース（`*T` 経由で使えるもの、promoted 含む、name ソート、昇格元 `Via` つき）。
- **`Def`/`DeclType`/`Initializer`** — `Def` は type decl の右辺、`DeclType` は var/const の注釈型、`Initializer` は初期化式。
- **`UnWrap`/`UnRef`/`Origin`** — `UnWrap` = declared 型1層、`UnRef` = pointer 1層、`Origin` = 全部追い切り。

## パターン集

- **optional なシンボル存在確認**: `LookupSymbol(pkg, name) == nil` で graceful 分岐（`Symbol` は trap するので判定には使わない）。
- **bound か source かを呼び出し側が制御**: `BoundOf`/`SourceOf`/`PackageOf`/`DirOf` の4 loader で経路をスペルできる（#876 の loader matrix が stub.go ヘッダに図示あり）。
- **walk の葉判定**: `SymbolID` → nil なら composite → `Children` 再帰。`NoDeclPath(path)` なら builtin/typeparam で打ち切り。それ以外は `LookupPackageOf`+`LookupSymbol` で decl 追跡（`extract-schema` の `follow` がこの形）。
- **bound shadow の迂回**: `BoundOf(path) != nil` なら `LookupSourceOf(path)` — 背後にソースがあればそちら、合成 bound path（`minigo.dev/inspect` 等）は nil が返るので graceful に落とせる（extract-schema `-pkg` の retry 形、PR #900）。
- **graceful な CLI**: `LookupPackageOf`/`LookupDirOf`/`LookupFileOf`/`LookupSourceOf`/`LookupSymbol` で nil→エラー exit に落とす。`PackageOf`/`DirOf`/`FileOf`/`SourceOf`/`Symbol` は trap なので「成功しなければ死ぬ」ことが確定している箇所にだけ。

## 関連

- `inspect/stub.go` — 宣言と doc コメント（契約の正本）
- `inspect/inspect.go` — ホスト側 view 型（`*Decl`/`Field`/`TypeExpr`/`Node`/`Method`/`Sig`/`File`/`Import`/`Comment`/`Position`）と `*Of` 系ホスト API（`TypeFieldsOf`/`MethodsOf`/`MethodSetOf`/`ImplementersOf` 等）。Go 側から同じ解析をしたいときはこちら。
- `docs/ja/minigo-architecture.md` — inspect が乗るパッケージ層（resolve/index）の全体像
