# Kovern docs

| Doc | What it's for | Audience |
|---|---|---|
| [`roadmap.md`](roadmap.md) | Current phase status — what's built, what's in progress, what's planned. The single source of truth for "is X done yet?" | Contributors, anyone evaluating Kovern |
| [`why-kovern.md`](why-kovern.md) | Problem statement and walkthrough scenarios — what Kovern prevents and why it's hard without it. Narrative/pitch style, not a spec. | New evaluators |
| [`development.md`](development.md) | Local dev setup, running tests, lint/security tooling, deploying to a local cluster. | Contributors |
| [`adr/`](adr/) | Architecture Decision Records — the *why* behind structural choices (controller-runtime, ServiceAccount scoping, in-memory ledger cache, the semantic detector's interface). Read before proposing a structural change. | Contributors making non-trivial changes |

For root-level orientation (what Kovern is, quick start, CRD reference) see the [repo README](../README.md).

## Keeping this in sync

`roadmap.md` is the only doc here that tracks implementation status and should be
updated whenever a phase's status changes. `why-kovern.md` and `development.md`
describe stable-ish things (the pitch, the dev workflow) and drift more slowly, but
if `why-kovern.md` describes behavior that isn't implemented yet, say so inline
rather than letting the narrative imply it — see its existing disclaimer note for
the pattern.
