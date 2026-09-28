# Contributing to pier

Thanks for pitching in. pier is deliberately small; the fastest way to land a
change is to keep it that way.

## Build and test

```
make          # cross-compiles the in-VM supervisor, embeds it, builds ./pier
make install  # install to $(go env GOPATH)/bin
make test     # go vet + go test -race
```

Always build with `make`, not `go build` — the supervisor binaries must be
embedded or session creation fails at runtime.

Requires: Go, `git`, OpenSSH, and the CLI of the cloud you test against
(`aws` v2 + `session-manager-plugin`, or `gcloud`). `pier doctor` checks the
runtime dependencies.

## Design ground rules

[docs/SPEC.md](docs/SPEC.md) is the source of truth for architecture and
open decisions — TODOs in code point there. The short version:

- **Lean over general.** No server, no daemon, no database: all state lives
  in instance tags and on the session's disk. Changes that add moving parts
  need a strong reason.
- **The cloud CLI, not the SDK.** pier already requires `aws` (for the SSM
  plugin) or `gcloud` (for the IAP tunnel), so the drivers shell out to them
  and v1 carries no SDK dependency.
- **No cloud credentials in the VM.** On AWS the instance role carries SSM
  and nothing else; on GCP sessions run with no service account. Anything
  that would put account credentials on a session box is out.
- **Guarded cloud-init.** The same user-data runs on stock and baked images;
  every install step is guarded so it no-ops when the image already has it.
  Guards must test something the stock image *lacks*.
- **Images are prebuilt, secrets are per session.** `pier bake` installs
  `.pier/bake.sh`'s tools and runs `.pier/setup.sh` on a checkout, scrubs
  everything pier pushed, and images the result. Setup then runs once per
  new session, on warm caches; a pooled session's claim and a wake never
  re-run it. pier does not chase language ecosystems in its default image.
- **General, not specific.** Every change works for any repo, any network
  and both clouds: no fixes shaped around one repo's setup, no product
  names in code or messages, and a fix in one driver needs the other's too
  (or a stated reason it doesn't have the problem).
- **One API, many frontends.** Capabilities live in `pkg/pier`, which never
  prints, exits or reads stdin; the CLI (`cmd/pier`), the app
  (`internal/tui`) and the coming Mac app render what it returns. Drivers
  don't write to the terminal either.
- **Failures are loud.** Setup outcomes, bake failures, unreachable
  sessions — every failure names itself and says where to look next.

## Code conventions

- CLI output goes through `internal/ui` (one accent color, the user's pick
  under Settings → Appearance; ANSI palette; lots of dim). Long-running
  commands print a bold header, step lines with elapsed time, and a green
  completion. `pier ls` output stays plain so it pipes cleanly; the app is
  the pretty view.
- Test-only code stays in `_test.go` files; nothing ships just to make
  something testable.
- Comments explain *why*, not *what*. Match the density already there.
- `go vet ./...` and `gofmt` clean before sending a PR. Some `modernize`
  suggestions (e.g. `SplitSeq`) are deliberately not applied.

## Sending changes

Open an issue first for anything beyond a small fix, so design questions get
settled before code review. PRs should include tests where behavior changed
and a one-paragraph description of the why. CI runs gofmt, `make test`, and
`make build` on every PR and must be green.

## Releasing (maintainers)

Releases are tag-driven. Homebrew builds from the source tarball, so there
are no prebuilt artifacts to manage.

1. Make sure CI is green on main.
2. Tag and push:
   `git tag -a vX.Y.Z -m "pier vX.Y.Z" && git push origin vX.Y.Z`.
   The release workflow re-runs the checks, publishes the GitHub release,
   and bumps the Homebrew formula in
   [usepier/homebrew-tap](https://github.com/usepier/homebrew-tap) to the
   new tag's tarball url and sha256.

The tap push authenticates with the `HOMEBREW_TAP_TOKEN` repository secret,
a fine-grained PAT with contents read/write on usepier/homebrew-tap (the
default `GITHUB_TOKEN` cannot push across repositories). If it expires,
mint a new one and update the secret.

Versioning is semver. While pier is 0.x, breaking changes bump the minor
version.
