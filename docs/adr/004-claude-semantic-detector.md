# ADR 004 — Use Claude for semantic livelock detection

**Status:** Accepted  
**Date:** 2026-05-26

## Context

Behavioral loop detection for AI agents requires understanding whether a sequence of
agent turns is semantically equivalent (a loop) or genuinely progressing.

Options evaluated:
1. **Hard timeout only** — kill any agent running longer than N minutes.
2. **Heuristic OTel-based detection** — count repeated tool calls and identical response hashes in a sliding window.
3. **Embedding-based cosine similarity** — vectorise turns with a local embedding model (bge-micro, Ollama); flag when cosine similarity exceeds a threshold.
4. **LLM-as-judge via Claude** — send the last N turns to Claude and ask it to classify whether the agent is stuck.

## Decision

Implement **both (2) and (4)**:
- Heuristic detection (option 2) is **always active** — no API cost, sub-millisecond evaluation.
- Claude semantic detection (option 4) is **opt-in** via `spec.detection.semantic.enabled: true`.

Claude model: `claude-haiku-4-5-20251001` by default (low latency, low cost).
The model is configurable per `LivelockPolicy`.

## Why Claude over local embeddings

- **Reasoning, not just similarity**: Claude can distinguish "the same search query with different phrasing" from "two queries that happen to share vocabulary but serve different goals".
- **No Ollama dependency**: Kovern is deployable on any cluster without requiring a sidecar GPU workload.
- **Configurable**: operators who want zero external API calls use heuristic-only detection.

## Interface design

The `claude.Detector` interface decouples the webhook and controller from the Anthropic SDK. Tests use a `NoOpDetector` or `mockDetector` without network calls. The real `AnthropicDetector` is injected at startup only when `ANTHROPIC_API_KEY` is present.

## Cost estimate

Haiku 4.5: ~$0.00025 per 1K input tokens. A 3-turn window with typical agent messages (~500 tokens total) costs < $0.001 per detection check. At 100 checks/hour per cluster: ~$0.10/day.

## Alternatives not chosen

- **Hard timeout only**: kills legitimate long-running tasks; doesn't prevent a looping agent burning $500 in 60 seconds.
- **Embedding cosine similarity**: requires Ollama in cluster; adds ~300ms per check via HTTP; cosine similarity is sensitive to vocabulary overlap but not semantics.
