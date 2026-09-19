# AGENTS.md

Before changing behavior or persistent state, read `docs/internals.md`. It
summarizes the architecture, invariants, resolution rules, and code ownership.
Update it when a change alters those contracts, but do not duplicate details
that are clear from the code or the user-facing README.

A user-visible specification change (CLI options, resolution rules, run/queue
semantics, etc.) generally needs updates in three places: `README.md` (feature
behavior), `docs/faq.md` (affected Q&A, if any), and `docs/internals.md`
(cross-cutting contracts). Check all three before considering the change done.

Job status/result display has two independent implementations that must be
kept in sync: `showJob`/`showRun` (`cmd/rotari/show.go`, CLI) and
`loadWebJobs` (`cmd/rotari/web.go`, web UI). Both walk the same fallback chain
(job's own `status`/`status.json` file, scheduler state, then
`summary.json`'s `Results`) to decide what a job's status/error is. When
adding a new source of truth or a new terminal state, update both, or one
will silently show less than the other.
