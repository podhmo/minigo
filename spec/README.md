# spec — formal models of minigo's implicit state machines

Quint specifications transcribing the implicit state machines of the
interpreter, starting with lazy package initialization. See
[../docs/lazy-lifecycle-model.md](../docs/lazy-lifecycle-model.md) for
the analysis these models transcribe, and
[../docs/sketch/ja/quint-memo.md](../docs/sketch/ja/quint-memo.md) for
tooling pitfalls (Japanese).

## Running

```sh
npm install -g @informalsystems/quint

# typecheck
quint typecheck spec/lazypkg.qnt

# randomized simulation (the rust backend needs a GitHub download that
# rate-limits easily; the typescript backend just works)
quint run spec/lazypkg.qnt --backend=typescript \
  --max-steps=60 --max-samples=300 --invariant=allInvariants

# witness: cyclic init's partial-serve behavior is reachable — this
# MUST report a violation; the violating trace is the witness
quint run spec/lazypkg.qnt --backend=typescript \
  --max-steps=40 --max-samples=500 --invariant=noPartialServeYet

# symbolic verification via Apalache (needs Java 21+)
quint verify spec/lazypkg.qnt --invariant=allInvariants
```
