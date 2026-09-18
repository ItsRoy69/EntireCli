# A gitlab `checkpoint_remote` can be read but not set — RESOLVED

Status: fixed 2026-09-18 on branch `peyton/gitlab-provider-gap`. Design and
investigation notes: `docs/superpowers/specs/2026-09-17-gitlab-checkpoint-remote-design.md`
(local, not checked in — `docs/superpowers/` is gitignored in this repo).

## What was wrong

The read path (`providerHost`, `cmd/entire/cli/checkpoint/remote/util.go`)
already resolved a `provider: "gitlab"` checkpoint_remote, but the only
documented way to configure one, `entire configure --checkpoint-remote
gitlab:owner/repo` (`parseCheckpointRemoteFlag`, `cmd/entire/cli/setup.go`),
rejected every provider except `"github"`.

## What was fixed (Phase 1: direct git transport)

- `parseCheckpointRemoteFlag` now accepts `gitlab` alongside `github`; error
  text and flag help/examples updated to match.
- Auth investigated and confirmed to need **no code change**: GitLab ignores
  the Basic-auth username for Personal/Project Access Tokens (the credential
  type `ENTIRE_CHECKPOINT_TOKEN` is used with), so the existing
  `x-access-token:<token>` header already works against gitlab.com. Verified
  against a real gitlab.com repo (both raw `git push`/`ls-remote` and a full
  real Claude Code session → checkpoint → `entire`-hook push, confirmed live
  on the remote via `git ls-remote`).
- `status.go` comment and `docs/testing/git-remote-test-plan.md`'s D-3 note
  updated to stop describing this as unresolved.

## What's still not supported (explicitly out of scope for this fix)

- `entire://` push-through mirror routing for gitlab origins
  (`gitremote.hostToForge` only maps `github.com`→`gh`).
- Trails API, repo protection, issue-linking, mirror-provider classification —
  these key off the control-plane's `provider` enum (`github` | `entire`,
  server-side, generated from a separate service's OpenAPI spec) and cannot be
  extended from this repo alone.
- Self-hosted GitLab (`gitlab.example.com`), same open question as
  self-hosted GitHub Enterprise.
- **entire.io discovery**: even with `checkpoint_remote` committed to
  `.entire/settings.json`, entire.io's checkpoint-discovery pipeline has no
  gitlab integration server-side today — checkpoints land safely on a gitlab
  `checkpoint_remote` but are not yet visible in the entire.io web app.

See the design doc for the full investigation, evidence, and phased roadmap.
