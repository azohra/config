# Architecture

Config has one boundary: a caller supplies a Git repository, and Config
reconciles the Mac against the document in that repository. Authentication,
identity recovery, repository selection, and any ceremony required before the
binary starts belong to the caller.

## Ownership

The machine repository owns policy and data. Its root `config.toml` is Config's
strict contract. Native mise declarations live under `mise/`; Config does not
parse or reinterpret them.

Mise owns the resources it declares and the order encoded by its bootstrap
phases and hooks. Config invokes one canonical standalone binary at
`~/.local/bin/mise`, scopes the repository's native `mise/` configuration to
each child process, and reports each bootstrap phase's status or the apply
result. Package names, repository purposes, commands, secret references, and
provider configuration remain opaque mise input. The standalone mise
installation is Config's execution substrate.

Config probes the phases separately rather than taking mise's aggregate.
The aggregate includes `repos`, where mise answers two questions at once:
whether a checkout exists, which is a local stat, and whether it matches its
remote, which is one network round trip per declared repository. Only presence
is machine setup, so Config asks mise for the declared paths and checks those
itself; freshness belongs to `config update`. Naming the phases means Config
must keep pace with mise, so a test pins the list to the phases mise offers.

Config owns the managed checkout, the reconciliation model, the `snapshots/`
storage convention, snapshot safety, the terminal interface, and its optional
native macOS capabilities. A capability is absent unless schema 1 declares it.
The document opts into a capability; it does not select the files Config uses
to persist that capability.

## Managed checkout

`config bootstrap <repository>` validates the locator before touching disk.
Repository locators may be HTTPS, SSH, `file://`, or absolute local paths and
carry only repository identity. Config rejects embedded credentials, queries,
fragments, ambiguous paths, and unsupported schemes.

The clone is built in a temporary sibling directory and validated before an
atomic rename to:

```text
~/Library/Application Support/Config/repository
```

Validation proves that the checkout root is not a symlink, the `origin` remote
matches the requested repository, the document declares the same repository,
and the checked-out branch and upstream match the document. A failed clone or
contract leaves no managed checkout behind.

## Reconciliation

Inspection runs independent probes concurrently and returns a `Report` of
resources plus repository snapshot state. A resource contains checks, a state,
the actions currently allowed, and details for the interface. Selection
validation runs against a fresh report immediately before any write, so a stale
or forged plan cannot invoke an action the current state no longer allows.

A declared capability without its canonical snapshot is `uncaptured`. Capture
reads live state and atomically creates the repository artifact. That first
capture establishes the tracked configuration; later inspections can compare
saved and live state. A fresh clone restores only artifacts already present and
leaves missing ones uncaptured.

Apply first converges mise with:

```text
mise bootstrap --yes --skip-dirty
```

The selected mise configuration owns any custom ordering through mise hooks.
Config then converges declared native macOS facts and executes selected
preference, Chrome PWA, and Dock actions. Independent failures are collected so
one resource does not hide the rest of the plan.

Dock and Chrome PWAs use three-way reconciliation:

```text
saved snapshot + live state + last agreement -> current, saved changed,
live changed, unknown, or conflict
```

Baselines live outside the repository under `~/.cache/config/state`. They are
written only when saved and live state agree. Captures use atomic writes; PWA
snapshots validate identifiers, URLs, schemes, icon digests, and plist input
before replacing saved state.

Preference plists use a different contract. Their declaration identifies the
application and defaults domain; Config derives
`snapshots/preferences/<id>.plist`. On an established Mac, Config can capture
current settings. A fresh clone may restore existing backups after mise installs
their applications. The restore validates the plist, checks the bundle, quits a
running application before import, and relaunches it afterward.

## Snapshot safety

Save stages the entire managed tree because the repository itself is the
snapshot. Before committing, Config verifies the repository root, declared
branch, exact `origin/<branch>` upstream, and remote identity. It then refuses
when a resource that owns snapshot content is unreadable or still waiting on a
bidirectional choice. Machine setup is reported in status but does not gate a
save: it converges live settings and records nothing in the repository, so a
package or checkout mise owns cannot make a commit wrong. Config commits under
the repository's Git policy and performs an append-only push. Push failure
leaves the local commit intact.

The repository locator is public configuration. Authentication stays in the
caller's Git environment or credential helper, outside the document, logs, and
Config state.

## Packages

`internal/config` owns the domain model, machine contract, subprocess boundary,
inspection, apply, baselines, repository lifecycle, snapshot, and update flow.
`internal/ui` renders the report, gathers explicit choices, and launches
operations in child processes. `cmd/config` owns argument parsing and selects
the CLI or terminal interface.

The release contains a single static binary for each supported macOS
architecture. GitHub Releases is Config's distribution channel; mise's GitHub
backend supplies the permanent installation selected by the machine repository.
