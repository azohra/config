# Architecture

Config has one boundary: a caller supplies a Git repository, and Config
reconciles the Mac against the document in that repository. Authentication,
identity recovery, repository selection, and any ceremony required before the
binary starts belong to the caller.

## Ownership

The machine repository owns policy and data. Its root `config.toml` is Config's
strict contract. `mise = true` opts into the Mise resource; native Mise
declarations then live under `mise/`, where Config does not parse or reinterpret
them. Without that declaration Config does not inspect, install, update, or
prune machine Mise state.

Mise is one Config resource. When declared, Config inspects the canonical
standalone binary at `~/.local/bin/mise`, installs the exact release whose
command vocabulary it tests, and delegates the declared tools, packages,
repositories, dotfiles, services, and lifecycle hooks to that binary. Package
names, repository purposes, commands, secret references, and provider
configuration remain opaque Mise input. Only commands issued for the Mise
resource receive the managed `mise/` selectors. Native resources and repository
operations clear those selectors, so storing Mise declarations in the machine
repository grants no authority over unrelated child processes.

Agent skills are another opt-in resource, independent of Mise. The machine
document declares repository sources, skill names, and requested agent targets.
User scope is implicit because Config manages a Mac; project-local skills
remain repository-owned. A target is not an isolation boundary: universal
agents share the canonical `.agents/skills` directory, while other agents get
their own links. Config delegates that layout to the exact
`skills@1.5.23` CLI it invokes through `npx`, so those implementation details
can evolve in one place. The adapter lives in Config's cache and is not a
globally installed machine tool. Its cache does not create another skill store:
Config and ordinary `npx skills` invocations converge on the same global paths
and skills CLI lock file. Config validates the current lock schema before any
mutation and stops on an unreadable or incompatible schema rather than using its pinned
adapter to recreate an older layout.

`config update` is the explicit version transition for Config and the resources
declared by the machine repository. A released Config uses a pinned,
checksummed Mise adapter in its own cache to acquire the latest stable Config
release with GitHub artifact attestation enabled. The adapter has no machine
configuration and is not the canonical machine executable. Config atomically
installs the acquired executable and continues the same update from it before
loading the machine document. The current release validates that document and,
when Mise is declared, reconciles that resource to its exact tested version and
updates the selected declarations. The software scope updates tools, packages,
and declared agent skills; the repository scope performs the networked
fast-forward of clean declared checkouts. The unqualified `config update`
selects both scopes. Unversioned development builds skip the Config release
transition. Release acquisition alone disables Mise's general
release-age delay because this explicit operation promises the latest release;
an exact resolved version, provenance verification, and downgrade refusal still
gate replacement.

Update planning has one domain contract for the CLI and terminal interface.
The plan records the exact resolved Config release and has a stable fingerprint
that excludes only its check time. The CLI applies the plan it prints. The
terminal interface passes the fingerprint to a locked child command, which
recomputes that scope once and refuses changed work before applying the newly
validated plan. Applying an accepted plan never repeats release discovery; it
acquires the recorded release exactly.

The terminal interface starts only the software check in the background. A
software selection reuses the completed plan or promotes that exact in-flight
request, while a different selection cancels it through the provider
subprocess context. Every request has a generation, so a late result cannot
replace a newer same-scope check. Completed operations invalidate cached update
status without starting another hidden
scan; only the ordinary machine report refreshes, without covering the result
screen.

Child operations use a Config-owned JSON event stream. Logger sections and
statuses carry explicit kinds, while provider bytes remain output events. The
terminal output model incrementally handles UTF-8, ANSI sequences, carriage
returns, and backspaces across write boundaries, and persists the final typed
spans. A successful self-update emits the installed version; the parent
interface then re-executes that command and reopens the persisted result.

The scoped child configuration is not a separate Mise installation. Mise's
tool store and tracked-configuration ledger remain user-wide. The machine
repository is the Mac's baseline authority, while a loadable repository-local
Mise document can remain an additional authority for that repository. Config
therefore delegates tool and package liveness to Mise's combined inventory
instead of treating the machine document as an exclusive manifest.

For a declared resource, Config asks once whether Mise can read the machine
document at all, because a phase probe answers the same exit code for real
drift and for a document Mise
rejects. Beyond that, Config probes the phases separately rather than taking
Mise's aggregate.
The aggregate includes `repos`, where Mise answers two questions at once:
whether a checkout exists, which is a local stat, and whether it matches its
remote, which is one network round trip per declared repository. Only presence
is resource state, so Config asks Mise for the declared paths and checks those
itself; freshness belongs to the explicit repository update scope. Naming the
phases means Config must keep pace with Mise, so a test pins the list to the
phases Mise offers.

Config is one writer at a time. Every command that writes takes an advisory
lock on the managed checkout before it starts, and refuses rather than
interleaving with another Config. The terminal interface holds no lock of its
own, because the commands it launches take it for the work they do.

An interrupt lands between writes, not inside one. SIGINT and SIGTERM end the
process once no file is mid-write; the staging, rename, and sync of a single
artifact is the section a signal waits out. A write still running after ten
seconds is reported and the process exits anyway.

A signal can still land between two files, and the rest of the design is what
makes that recoverable. Ownership is recorded before the hook it claims. A
fact the next write destroys is recorded before that write. Config names what
it stages, and the next run sweeps it.

Config owns the managed checkout, the reconciliation model, the `snapshots/`
storage convention, snapshot safety, the terminal interface, and its optional
capabilities. A capability is absent unless schema 4 declares it. The document
opts into a capability and supplies personal values or payloads; Config owns
where and how those declarations converge.

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
contract leaves no managed checkout behind. Before installation, Config gives
the clone a local restore identity and atomically records pending progress under
`~/Library/Application Support/Config/restore`. A later bootstrap resumes only
when the repository, checkout, cloned commit, and versioned declaration plan
all match and the checkout remains clean. A checkout whose commit has moved
on, which is what saving a snapshot does, can no longer resume that restore,
so bootstrap abandons the record and says so instead of refusing forever.
Local changes stay a refusal: clear them and the restore resumes. Successful
steps are recorded individually; completion closes the restore permanently for
that checkout.

Bootstrap atomically installs the running released executable at
`~/.local/bin/config` after checkout validation and before restoration. Config
therefore owns its permanent command even when restoration needs another run.
`config path` exposes the canonical checkout without requiring it to exist.

`config install` is the handoff verb one release calls on the next. The running
Config acquires a newer release and runs that executable's own `install`, so the
spelling is a contract between two versions rather than an interface for a
person: renaming it would strip the ability to self-update from every Config
already on a Mac. It is spelled without a leading `--`, unlike Config's other
internal arguments, for that reason alone.

## Reconciliation

Inspection runs independent probes concurrently and returns a `Report` of
resources plus repository snapshot state. A resource contains checks, a state,
the actions currently allowed, and details for the interface. Selection
validation runs against a fresh report immediately before any write, so a stale
or forged plan cannot invoke an action the current state no longer allows.

A declared capability without its canonical snapshot is `uncaptured`. Capture
reads live state and atomically creates the repository artifact. That first
capture establishes the tracked configuration; later inspections can compare
saved and live state. A pending bootstrap restores only artifacts already
present and leaves missing ones uncaptured. It reports an artifact it cannot
read rather than stopping, because the capabilities beside it may restore
perfectly well. Completed steps stay complete across retries. Every resource is
attempted and checkpointed independently. A missing snapshot completes as
uncaptured; a missing application leaves its dependent step pending for a later
retry. A failed Mise resource does not prevent native resources from
converging.

Applying the Mise resource installs the tested standalone release when it is
missing or incompatible, then delegates machine convergence with:

```text
mise bootstrap --yes --skip-dirty
```

Applying agent skills asks the pinned npx adapter for one global inventory that
includes every installed agent placement. An existing skill from the declared
source is adopted without a reinstall; missing skills or agent placements are
reconciled by source. Config
records the canonical path and a digest of each adopted tree under Application
Support. A compatible same-source tree changed by another skills CLI is drift
that Apply can adopt without rewriting it. Explicit update and prune refuse
unadopted content, while a different source remains a conflict. Inspection is
offline and uses Config's own npm cache; apply and explicit software updates may
fetch the pinned package and declared skill repositories.

Reconciliation validates the adapter once and refreshes that inventory only
after a mutation. The adapter reports display names for agent placements, so
Config normalizes those names and falls back to a scoped query only for an
exceptional name. Explicit updates reuse the preflight inventory and each tree's
digest, then perform one post-update inventory and digest pass. Config writes
the ownership manifest only when its canonical bytes changed.

When repository hooks are declared, Config prepares their clone template before
Mise runs and supplies that template only to Mise's child Git processes. It
sweeps the declared repositories after bootstrap so existing and newly created
checkouts converge to the same hook bodies. The selected Mise configuration
owns any custom ordering inside that resource through Mise lifecycle hooks.
The declared native macOS facts are a separate resource. None of them touches
anything Mise installs, so an unusable Mise version neither hides their status
nor stops them converging. A fact Config cannot read is reported rather than
written over, and a fact that fails is an advisory, so the facts beside it and
the steps after it still run. Config executes every other selected resource
independently and collects failures so one resource does not hide the rest of
the plan.

Repository hooks are authoritative one-way state. A declaration names a hook
and an executable source inside the managed repository. Config installs real
copies into its template and its own checkout. When Mise is declared, it also
installs them into the common Git directory of each repository Mise declares.
Repository hooks receive that inventory from the Mise adapter; they do not
select or parse Mise configuration themselves. A small manifest beside each
copy records Config's last installed digest. That lets a later source change
refresh the copy while undeclared hook names remain untouched. A
repository-owned same-name hook, linked hooks directory, or `core.hooksPath`
redirect is reported and preserved. Config resolves common directories through
Git, so linked worktrees share the same hook without a path back into the
machine repository.

Finder Favorites, Dock, and Chrome PWAs use three-way reconciliation:

```text
saved snapshot + live state + last agreement -> current, saved changed,
live changed, unknown, or conflict
```

The Finder Favorites snapshot owns the label, path, and order of every
resolvable path-backed directory entry. It stores the managed checkout as a
symbolic target and home-relative paths portably; unresolved or non-directory
entries remain opaque and outside Config's ownership. Restore verifies every
target before writing, uses the native macOS shared-file-list API to insert or
move desired entries before removing extras, and restores the original layout
if the result cannot be verified. Config loads the API at runtime and
reports the resource unavailable if macOS no longer exposes it.

Each side reduces to the fact Config tracks before it is compared: the apps
in the Dock, the PWAs installed, and the ordered path-backed Finder Favorites.
A PWA's name, URL, icon, and schemes are
kept because a restore rebuilds the bundle from them, but Chrome owns that
content and rewrites it on its own schedule, so comparing it would report
Chrome's churn as a choice.

Dock inspection exports the current user's `com.apple.dock` domain and decodes
`persistent-apps` without writing. Restore reorders only application tiles,
reuses their complete existing dictionaries, and carries non-app tiles through
unchanged. Missing applications receive a minimal file tile with a unique
identifier. Config writes only the `persistent-apps` key, rereads its app paths,
and restores the original key when verification fails. The Dock restarts once,
after a verified change.

Baselines live outside the repository under `~/.cache/config/state`, and are
written only when saved and live state agree. That path comes from the home
directory alone. Config's own state is deliberately not relocatable by the
caller's environment, because the checkout lock lives beside the baselines: a
cache directory that could move on its own would give two Configs converging one
Mac two different locks. Captures use atomic writes;
Finder snapshots validate names, portable targets, and unique absolute paths,
while PWA snapshots validate identifiers, URLs, schemes, icon digests, and
plist input before replacing saved state.

Preference plists use a different contract. Their declaration identifies the
application and defaults domain; Config derives
`snapshots/preferences/<id>.plist`. On an established Mac, Config can capture
current settings: the whole domain, unfiltered, into a repository Config
commits and pushes. A domain that holds nothing is refused, because `defaults`
answers with an empty dictionary for a domain that does not exist. A pending
bootstrap attempts existing backups independently; an application that is not
yet installed leaves only its own restore pending. The restore validates
the plist, checks the bundle, quits a running application before import, and
relaunches it afterward.

## Snapshot safety

Save stages the entire managed tree because the repository itself is the
snapshot. Before committing, Config verifies the repository root, declared
branch, exact `origin/<branch>` upstream, and remote identity. It then refuses
when a resource that owns snapshot content is unreadable or still waiting on a
bidirectional choice. Authoritative live resources are reported in status but
do not gate a save: they record nothing in the repository, so a package or
checkout Mise owns cannot make a commit wrong. Config writes the
fixed `Update machine snapshot` subject, commits under the repository's Git
policy, and performs an append-only push. Push failure leaves the local commit
intact.

The repository locator is public configuration. Authentication stays in the
caller's Git environment or credential helper, outside the document, logs, and
Config state.

## Pruning

Pruning crosses two ownership domains without merging them. When declared,
Mise computes prunable tool versions and provider-owned packages from its
shared inventory. Config asks Mise where its state lives, reads the tracked and
trusted configuration ledgers there to name the links that no longer resolve,
and leaves every deletion to Mise. Config discovers package managers from Mise
rather than maintaining its own provider list. A provider with no prune
operation is reported and left alone. If declared Mise is unavailable, its
cleanup and dependent repository inventory are reported and preserved while
Config still plans its own state. Undeclared Mise is never probed.

Config directly removes only artifacts with verifiable Config provenance. An
undeclared agent or skill placement is eligible only while the live source,
canonical path, and tree digest still match Config's ownership record. Removal
is delegated to the same pinned CLI so universal and agent-specific layouts
remain its responsibility. An undeclared repository hook is eligible when its
bytes still match the digest in Config's adjacent ownership manifest; a changed
or non-regular hook and its record are preserved. A baseline is eligible only
after its schema and resource identity validate and the machine no longer
declares that capability. A completed restore record is eligible only when it
validates and belongs to a managed-checkout identity other than the current
one. Pending and current
records remain. A marker recording a step an interrupted run still owes is
eligible only when the document no longer declares the capability that would
act on it.

The CLI and terminal interface print the complete plan before mutation. The
terminal interface confirms on its own screen and then invokes the explicit
non-interactive apply form; a redirected CLI remains preview-only without
`--yes`. Apply recomputes the plan and refuses it if the candidate set or any
ownership digest changed after preview. Mise performs its own deletions; Config
revalidates every file it removes immediately before the operation.

## Packages

`internal/config` owns the domain model, machine contract, subprocess boundary,
typed operation events, inspection, apply, baselines, repository lifecycle,
snapshot, and update coordinator. `internal/ui` renders those contracts,
gathers explicit choices, previews cleanup, and launches locked operations in
child processes. `cmd/config` owns argument parsing and selects the CLI or
terminal interface.

The release contains a single static binary for each supported macOS
architecture. GitHub Releases is Config's distribution channel. The caller
supplies a verified binary for the initial handoff; explicit updates acquire a
verified release through Config's cache-owned release adapter, independently of
the machine's Mise declaration or canonical Mise command. Config installs the
executable as its permanent command. Each release also
ships Config's license and the license material required to redistribute its
dependencies. A read-only canary on a disposable macOS CI runner exercises the
native Finder Favorites boundary without changing the runner's state.
