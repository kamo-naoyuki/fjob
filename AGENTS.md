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

Common mistakes to avoid when editing this project:

- Treat project names, run IDs, and job IDs as path elements, not as arbitrary
  strings. Validate them before any `filepath.Join`, `os.ReadFile`, or
  `os.Stat` call. A value that is empty, `.`/`..`, absolute, or contains `/`
  or `\` must be rejected, even if it looks harmless on one OS.
- Do not duplicate validation ad hoc in one CLI path and forget the web or API
  layer. The same guard must be reused across CLI, server, and web handlers,
  otherwise a request path can bypass the stricter client-side checks.
- When a status/result source is displayed in two places, keep those code paths
  synchronized. A change in the fallback chain or terminal-state rules must be
  mirrored in both `show` and `web` implementations.
- A small-looking change to path construction can become a traversal bug if it
  only checks `filepath.Base` or only rejects `/` while ignoring `\`.
  The project intentionally blocks both separators to keep state under the
  resolved base directory.
