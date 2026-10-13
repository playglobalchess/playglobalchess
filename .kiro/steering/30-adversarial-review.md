---
inclusion: always
---
# Adversarial self-review — the mechanical procedure (always run before "done")

The build manual §0 requires a recursive self-review before declaring any unit of
work done. This file makes it **mechanical**, because the abstract version ("state
your concerns") is too easy to pass with a glance. The author of a change is the
weakest reviewer of it — you cannot see your own assumptions — so a vague
self-check converges on "looks good" while real bugs sit on paths you never
pictured. The findings that keep slipping through are not subtle; they are the
ones a *structured* pass would have forced you to look at.

## Why this exists (real misses, so the lesson is concrete)

On the game-server live session, author self-review passed with green tests, yet
the owner's review found, in sequence:
- a stale clock projection (a state field never written on the move path),
- an unretained terminal (a field read but never set),
- **idempotent-re-ack corruption** — the pipeline's own documented re-ack shape
  (Accepted with empty FEN/zobrist) blanked the board on a routine client resend,
- two divergent takeback cut algorithms, and
- a reconnect that forgot pre-rebuild think time.

Every one was readable in the code against a collaborator's stated contract.
Green author-written tests did not catch them because those tests re-confirmed the
mental model that *held* the blind spot. "My tests pass + it compiles" is NOT the
diminishing-returns stopping point.

## The pass — run ALL of it before "done"

**1. Callee contract enumeration.** For every function/interface you CALL, read
its OWN doc/contract and list its distinct output shapes and error/edge branches.
Prove your code handles EACH. The re-ack bug was one branch of `SubmitMove`'s
documented contract that the caller ignored. If a callee says "idempotent",
"retry", "may return empty", "nil on X" — there is a branch you must handle.

**2. Failure / retry / restart walk.** Explicitly trace these scenarios, which
happy-path tests never exercise:
- a lost ack and the client RESENDING the same request;
- a crash/error BETWEEN two steps (did step 1 half-apply?);
- RECONSTRUCTION / failover mid-operation (what state is lost vs recovered from
  durable storage? is a timestamp/baseline silently reset?);
- a CONCURRENT caller (is the mutation actually serialized?).

**3. Per-field write-coverage.** For EVERY state/projection field, ask: *what
writes it, and is it written on every path that should?* Walk each code path and
tick the field off. The stale-clock and unretained-terminal bugs fall straight
out of this question.

**4. Test the edge, not the center.** For each contract edge found in 1–3, write
the test that FAILS FIRST (before the fix), then confirm the fix flips it green. A
test that only demonstrates the happy path you imagined proves nothing about what
you didn't imagine. An invariant test must assert the WHOLE invariant (the
snapshot==fold test that didn't compare the clock field is the cautionary tale).

## The anti-self-bias net (load-bearing changes)

For a load-bearing chunk — server hot path, state machine, concurrency,
persistence, auth, anything correctness-critical — self-review is not enough.
**Spawn a context-free independent reviewer** before declaring done:
`spawn_run` with `include_memory=false`, handed ONLY the diff plus the
specs/contracts it touches. Its value is precisely that it did not see your
reasoning, so it cannot inherit your blind spot. Treat its findings as real until
disproven. **The human reviewer must not be the net** — their time is the cost of
every miss this pass would have caught.

## Close-out

Only after this pass finds nothing material (not after tests merely go green) is a
chunk "done" per §0/§4. In the final summary, state which of 1–4 you ran and what
they surfaced, so the review is visible, not assumed.
