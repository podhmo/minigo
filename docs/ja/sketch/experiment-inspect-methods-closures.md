# メソッドとクロージャを含む補助関数の実験

## 計画（実装前）

前の関数ボディ実験はコミット `b5e5a57` に保存した。この実験は `experiments/httpinspect` を拡張し、通常VMには手を入れずに、構文ビューだけでメソッドとクロージャをどこまで扱えるかを確認する。

### 検証する仮説

- 具体的なソースstructの型が分かれば、既存の Methods/Signature/Fields と Body でレシーバーを補助関数の引数として解析できる。
- メソッド値はレシーバーを保存する必要がある。値レシーバーとポインターレシーバーで、後からフィールドを書き換えたときの結果が違う。
- クロージャには生成時の値のスナップショットだけでは足りない。変数の束縛を捕捉し、後の代入やクロージャ内部の代入を反映する必要がある。
- 別関数から返されるクロージャでは、生成した関数のファイル/import文脈とローカルな束縛を、戻った後も保持する必要がある。
- 分岐で環境を複製するとき、捕捉した変数の参照が別の経路へ漏れないようにする必要がある。

### 実装方針

解析側にcallable（直接関数、束縛されたメソッド、FuncLit）を追加する。名前から値へのmapを、名前から束縛IDへのmapと、束縛IDから抽象値へのstoreに分ける。クロージャは名前と束縛IDを捕捉する。各経路のstoreを分け、呼び出し後は抽象値のunionで戻り値と副作用を合流する。

structは明示的な型とkeyedフィールドの値を持つ小さいモデルとする。直接のメソッド、メソッド値、メソッド式、関数値引数、返されたクロージャを試す。FuncLitの引数/戻り値はSyntaxChildrenから読む。追加のinspect APIが本当に必要になった場合のみ、その不足と設計を記録して追加する。

全Goの型推論、interfaceの完全なメソッド集合、埋め込みとpromoted methods、generics、ループ不動点、一般的なヒープ解析は今回の対象外。未解決の呼び出しは診断する。既存の深さ/ステップ上限と、標準ライブラリを要約で止める方針を維持する。

### 実験ケース

1. 同一/別パッケージの具体的なstructのメソッドからrequestを読む。
2. メソッド値とメソッド式を補助関数として呼ぶ。
3. レシーバーのフィールドを書き換えた後の値/ポインターメソッド値を比較する。
4. クロージャのrequest/key捕捉、生成後のkey書き換え、内側からの書き換え。
5. 補助関数から返されたクロージャ、関数値引数、入れ子のクロージャ。
6. シャドーイング、分岐で捕捉値が異なるケース、自己再帰クロージャ、未解決interface/埋め込み。
7. 候補・呼び出しの引数/戻り値・診断・ソース読み込み境界をテストする。make format/lint/testを実施する。

## 実施記録

実装中の発見、意思決定、計画との差、結果をここに追記する。

### 実装と到達点

`analyze.go` の環境を束縛IDと経路別storeに分け、`callables.go` に関数/メソッド/クロージャ、ソースstructのフィールド、値のコピー、ローカル変数のアドレスを追加した。`order.go` は副作用を持つ呼び出しの前後で変わる純粋な読み取りを扱う。

**この範囲では inspect のAPIを追加する必要はなかった。** 既存の Body/SyntaxChildren のRole、Methods/Signature/Fields、Owner/import文脈で入口の情報を得られた。FuncLit の Params/Results/Body も既存ノードから取り出せた。束縛ID、捕捉、store、呼び出しの方針は解析側で実装した。通常のcompiler/VMは変更していない。

具体型が追跡できるstructのメソッドは、レシーバーを抽象値として束縛して本体に入る。メソッド値はレシーバーを保持し、メソッド式は先頭の引数をレシーバーにする。値レシーバーはコピーし、ポインターレシーバーは保存場所を参照する。interfaceに具体的なReaderの値を入れたケースも、その値の由来からReadへ進めた。ただし未知のinterface値やpromoted methodsの一般的な解決はできない。

クロージャはOwner、Body、引数/戻り値、見えている名前と束縛IDを保持する。生成した関数が戻っても、捕捉先のslotはstoreに残る。クロージャの呼び出し時にそのslotの現在の値を読むので、生成後の代入も反映される。内側からの代入は同じslotを書き換える。呼び出しトレースには `captures` を追加し、呼び出し入口での値を観測できるようにした。

捕捉対象は厳密な自由変数だけではなく、生成時に見える全束縛を保守的に保持している。束縛IDは実験内の動的なIDで、汎用的なinspectの静的binderを作ったわけではない。

### 想定外の出来事と意思決定

1. **値のスナップショットではクロージャを扱えなかった。** `read := func(){query(r,key)}` の後に `key="after"` とすると、実際に読むのはafter。束縛そのものを捕捉する構造に変更した。シャドーイングは別IDになる。
2. **分岐で同じ可変cellを共有すると別経路の値が漏れる。** storeを経路ごとに複製し、callableはstoreを直接指さずIDだけを保持する方式にした。補助関数が戻るときはstoreと戻り値をunionする。条件の相関を失う過大近似であり、全ヒープの精密な解析ではない。
3. **フィールドだけを保持するポインターモデルでは変数全体の再代入を扱えなかった。** `read:=x.PointerRead; x=Reader{...}; read()` はxの新しい値を読む。一方 `p:=&Reader{...}; read:=p.PointerRead; p=&Reader{...}; read()` は元の指し先を読む。ローカルstructのアドレスは変数の束縛ID、既存ポインター値はその指し先のフィールドslotとして区別した。
4. **ポインター経由の値メソッド式では、式のレシーバー型と宣言のレシーバー型が違う。** `(*Reader).Read(p)` の式はポインターを受けるが、Readの本体は値レシーバー。式の引数形状を確認した後、本体に渡す値はコピーするようにした。
5. **想定した評価順がネイティブGoと一致しなかった。** 以下の独立した比較を追加して初めて分かった。単純に期待値をGoの出力へ変更せず、読み取り順の不確実性を診断して候補をunionする方針にした。
6. **一般的なアドレス/メソッド集合は範囲を超えた。** `h.Value.PointerRead` の自動アドレス、埋め込みstructのpromoted methods、具体型が不明なinterface、ポインターのポインターは診断で止める。明示的な境界として残し、完全な型検査を導入しない。

### 独立したGoとの比較と評価順の発見

`native_test.go` は同じfixtureを一時的なGo moduleへコピーし、通常のGoで実行する。init panicを削除し、補助関数Queryだけをキーを記録して"1"を返す観測境界に置き換える。メソッド、クロージャ、代入、returnのコードはそのまま。各ハンドラーにnil/non-nilのrequestを渡して分岐の両側を観測する。これはリクエストAPI自体の実行検証ではなく、キーが届くまでのGoの束縛/呼び出しの意味論を比較する実験である。元のfixtureにはinit panicを残し、抽象解析で初期化されないことは別のテストで検証する。

25ケースのうち、当初は3ケースで差が出た:

| ケース | 最初の抽象解析の候補 | ネイティブGoの観測 |
| --- | --- | --- |
| `p.Key=move()` でmoveがpを別のReaderへ変える | second、written-first | first、written-first |
| `x.ReadAfter(change())` でchangeがxのKeyを変える | before-argument、after-argument | after-argument |
| `ReadWithArg(x,change())` | before-copy、after-copy | after-copy |

呼び出しが字句順で並ぶことと、普通の変数の読み取りがその呼び出しより前に確定することは同じではない。読み取りと呼び出しの相対順が指定されない場合があることを [Go仕様のOrder of evaluation](https://go.dev/ref/spec#Order_of_evaluation) と [Assignments](https://go.dev/ref/spec#Assignments) で確認した。上の観測値は使用したGoコンパイラーの結果で、唯一の言語仕様上の結果と解釈しない。

修正後は副作用を持つ呼び出しを二度実行せず、再読しても呼び出しを含まない式だけを前後で比較する。レシーバー/引数/代入先が変わった場合は候補をunionし、`evaluation order may change ...` の診断とincomplete=trueを返す。複数の代入先候補は元の値も残す弱い更新になる。AssignmentOrderの候補はfirst、second、written-firstとなる。

最終的なネイティブ比較は22ケースで候補名が完全一致、評価順に依存する3ケースでは観測値が候補に含まれ、かつ不確実性が診断されることを確認する。全ての評価順を網羅した解析ではない。中間の状態・複数の副作用・複雑な式の相関にはeffect解析と制御フローの整理が必要。

### 追加した29ケース

| ケース | 結果 |
| --- | --- |
| LocalMethod / ImportedMethod | 同一/別パッケージのレシーバーからqueryを取得。後者はAtoiまで伝播 |
| MethodValues | 値メソッド値はold、ポインターメソッド値はnew |
| MethodExpression / PointerMethodExpression | 型から作るメソッド式の引数とレシーバーを束縛 |
| PointerValueMethodExpression | ポインター型のメソッド式から値メソッドを呼ぶ |
| PointerReassign / AddressReassign | 変数全体を再代入した新しい値を読む |
| PointerValueReassign | ポインター変数の再代入前の指し先を保持 |
| PointerWrite / ReceiverCopy | ポインター経由の書き換えと、値渡しによる独立コピーを区別 |
| ClosureReassign / ClosureWrite | 生成後の代入とクロージャ内からの代入を反映 |
| ClosureShadow | inner/outerを別の束縛として追跡 |
| EscapedClosure / NestedClosure | 戻った関数のローカル捕捉と入れ子を追跡 |
| ReceiverClosure | 値レシーバーを捕捉したクロージャはboundのまま |
| BranchCapture / BranchCallable | 別経路のキー/関数値が混ざらず候補に残る。条件式はunknown診断 |
| CallableAlternatives / ClosureReturnEffects | 複数のcallableと早期return後の捕捉書き込みをunion |
| RecursiveClosure | 自分の束縛を捕捉して再帰。深さ上限の診断で停止 |
| KnownInterface | 具体的な値が追跡できるinterfaceからReadへ進む |
| InterfaceUnknown / Embedded / NonlocalReceiver | 未解決interface/埋め込み/非ローカルの自動アドレスを診断 |
| AssignmentOrder / MethodEvaluationOrder / ArgumentCopyOrder | 評価順の不確実性を候補と診断に残す |

前回の13ケースも維持し、元のClosureケースは今回解析可能になった。新ケースは29、合計42ケースの期待値を検証している。ケースごとのcalls/stepsは構文読み取りを含む抽象操作の数で、性能指標とは扱わない。

### inspectと専用解析VMへの示唆

この範囲で不足していたのは、ASTへの入口よりも、解析側の束縛・値・保存場所・副作用のモデルだった。単純にメソッドへ入る操作や関数リテラルのBodyを読むAPIを増やすだけでは捕捉後の代入や別経路の隔離は解決しない。

汎用のinspectへ追加すると有用なのは、宣言/参照/捕捉の静的な束縛ID、scope情報、解決できる呼び出し先の候補と未解決理由。HTTP固有の要約や経路別storeは解析側に置く。自由変数の抽出も将来のbinderで明示できると不要な捕捉を減らせる。

専用VM/IRを作るなら、Bind/Capture/Load/Store、値コピー、Address/Project、Call/Returnに加え、副作用と読み取りの順序制約を表せる必要がある。命令を直列に並べるだけでは、今回見つかった評価順の不確実性を一つの順に固定してしまう。現段階では通常VMを改造するより、実験のstore/callableモデルを整理し、effectsと制御フローを導入する方が次の調査として有益と考える。

### 制限と残件

- captureは全可視束縛を保持し、storeには不要になったslotも残る。自由変数/lifetime解析は未実装。
- 関数の出入りと代入で値コピーが重複する。ステップ上限はslot数やprinterの仕事量を制限しない。
- fieldへの複数の指し先と分岐合流で相関を失う。一般のヒープ/グローバル/opaque callbackの副作用は要約できない。
- メソッド集合とlocal addressの対象は限定的。完全な型推論・alias・promoted methods・generics・未知interfaceは未実装。
- 評価順の前後比較は今回の例を扱う小さいモデル。完全な順序制約・effect解析は次の実験対象。
- required/schemaの契約の確度や完全なOpenAPI生成は前回と同じく未実装。

これらをTODO.mdの未完了タスクに記載し、完了したメソッド/クロージャの実験はImplementedにまとめる。

## 検証結果

- `make format`: 成功（goimports）。
- `make lint`: 成功（staticcheck）。
- `make test`: 成功。ルート全体、examples/task-run、examples/convert-defineを含む。
- `go build ./experiments/httpinspect/testdata/handlers ./experiments/httpinspect/testdata/helpers`: 成功。fixtureが通常のGoとしてコンパイルできることも確認した。実行やinitはしない。
- `git diff --check`: 成功。
- 29ケースの候補/診断、前回13ケース、25ケースのネイティブ比較、4ケースのlazy loadingを検証した。
- lazy loadingのresolver記録は対象とhelpersの2パッケージのみ。解析したパッケージはindexedのままで、net/http/strconvのソースには入らない。

再現はリポジトリルートで:

```sh
go run ./experiments/httpinspect/cmd/httpinspect \
  github.com/podhmo/minigo/experiments/httpinspect/testdata/handlers EscapedClosure

go test ./experiments/httpinspect \
  -run 'Test(MethodsAndClosures|CallableLazyBoundaries|NativeCallableSemantics)$' -count=1
```

キャッシュの書き込み制限を避けるため、検証時は `GOCACHE=/tmp/minigo-body-go-cache` とlint用の `STATICCHECK_CACHE=/tmp/minigo-body-staticcheck-cache` を指定した。GOPATHは変更していない。一時的なoracle用sourceと実行キャッシュはコミットしない。
