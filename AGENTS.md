# AGENTS.md

Before changing behavior or persistent state, read `docs/internals.md`. It
summarizes the architecture, invariants, resolution rules, and code ownership.
Update it when a change alters those contracts, but do not duplicate details
that are clear from the code or the user-facing README.

A user-visible specification change (CLI options, resolution rules, run/queue
semantics, etc.) generally needs updates in three places: `README.md` (feature
behavior), `docs/faq.md` (affected Q&A, if any), and `docs/internals.md`
(cross-cutting contracts). Check all three before considering the change done.
