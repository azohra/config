# Architecture

Config has one boundary: a caller supplies a Git repository, and Config
reconciles the Mac against the document in that repository. Authentication,
identity recovery, repository selection, and any ceremony required before the
binary starts belong to the caller.

## Ownership

The machine repository owns policy and data. Its root `config.toml` is Config's
strict contract. Native mise declarations live under `mise/`; Config does not
parse or reinterpret them.

Mise owns the resources the machine repository declares and the order encoded
by its bootstrap phases and lifecycle hooks. Config invokes one canonical
standalone binary at
`~/.local/bin/mise`, scopes the repository's native `mise/` configuration to
each child process, and reports each bootstrap phase's status or the apply
result. Package names, repository purposes, commands, secret references, and
provider configuration remain opaque mise input. The standalone mise
installation is Config's execution substrate, and Config accepts exactly the
version whose command and phase vocabulary the release tests.
Config disables mise's ambient auto-update behavior in child processes;
`config update` is the explicit version transition for Config, mise, and the
resources declared by the machine repository. A released Config first repairs
canonical mise to the version it knows, then uses that isolated substrate to
acquire the latest stable Config release with GitHub artifact attestation
enabled. It atomically installs that executable and continues the same update
from the installed command before loading the machine document. The current
release validates that document, normalizes mise to the exact version it was
tested against, and updates the selected resources. The software scope updates
tools and packages; the repository scope performs the networked fast-forward of
clean declared checkouts. The unqualified `config update` selects both scopes.
Unversioned development builds skip the Config release transition. Release
acquisition alone disables
mise's general release-age delay because this explicit operation promises the
latest release; an exact resolved version, provenance verification, and
downgrade refusal still gate replacement.

The scoped child configuration is not a separate mise installation. Mise's
tool store and tracked-configuration ledger remain user-wide. The machine
repository is the Mac's baseline authority, while a loadable repository-local
mise document can remain an additional authority for that repository. Config
therefore delegates tool and package liveness to mise's combined inventory
instead of treating the machine document as an exclusive manifest.

Config asks once whether mise can read the machine document at all, because a
phase probe answers the same exit code for real drift and for a document mise
rejects. Beyond that, Config probes the phases separately rather than taking
mise's aggregate.
The aggregate includes `repos`, where mise answers two questions at once:
whether a checkout exists, which is a local stat, and whether it matches its
remote, which is one network round trip per declared repository. Only presence
is machine setup, so Config asks mise for the declared paths and checks those
itself; freshness belongs to the explicit repository update scope. Naming the
phases means Config must keep pace with mise, so a test pins the list to the
phases mise offers.

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
capabilities. A capability is absent unless schema 2 declares it. The document
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
perfectly well. Completed steps stay complete across retries. Converging mise
stays the one deliberate stop: it installs the applications every later step
restores into. A missing snapshot completes as uncaptured; a missing application
leaves its dependent step pending for a later retry.

Apply first converges mise with:

```text
mise bootstrap --yes --skip-dirty
```

When repository hooks are declared, Config reconciles them first. It prepares
their clone template before mise runs and supplies that template to mise's
child Git processes. It sweeps the declared repositories after bootstrap so
existing and newly created checkouts converge to the same hook bodies. The
selected mise configuration owns any custom ordering through mise lifecycle
hooks. The declared native macOS facts converge alongside mise rather than
behind it. None of them touches anything mise installs, so a mise version
Config cannot use neither hides them from status nor stops them converging. A
fact Config cannot read is reported rather than written over, and a fact that
fails is an advisory, so the facts beside it and the steps after it still run.
Config then executes the selected Finder Favorites, preference, Chrome PWA,
and Dock actions. Independent failures are collected so one resource does not
hide the rest of the plan.

Repository hooks are authoritative one-way state. A declaration names a hook
and an executable source inside the managed repository. Config installs real
copies into its template, its own checkout, and the common Git directory of
each repository mise declares. A small manifest beside each copy records
Config's last installed digest. That lets a later source change refresh the
copy while undeclared hook names remain untouched. A repository-owned
same-name hook, linked hooks directory, or `core.hooksPath` redirect is reported
and preserved. Config resolves common directories through Git, so linked
worktrees share the same hook without a path back into the machine repository.

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

Baselines live outside the repository under `~/.cache/config/state`. They are
written only when saved and live state agree. Captures use atomic writes;
Finder snapshots validate names, portable targets, and unique absolute paths,
while PWA snapshots validate identifiers, URLs, schemes, icon digests, and
plist input before replacing saved state.

Preference plists use a different contract. Their declaration identifies the
application and defaults domain; Config derives
`snapshots/preferences/<id>.plist`. On an established Mac, Config can capture
current settings: the whole domain, unfiltered, into a repository Config
commits and pushes. A domain that holds nothing is refused, because `defaults`
answers with an empty dictionary for a domain that does not exist. A pending
bootstrap may restore existing backups after mise installs their applications.
The restore validates
the plist, checks the bundle, quits a running application before import, and
relaunches it afterward.

## Snapshot safety

Save stages the entire managed tree because the repository itself is the
snapshot. Before committing, Config verifies the repository root, declared
branch, exact `origin/<branch>` upstream, and remote identity. It then refuses
when a resource that owns snapshot content is unreadable or still waiting on a
bidirectional choice. Machine setup is reported in status but does not gate a
save: it converges live settings and records nothing in the repository, so a
package or checkout mise owns cannot make a commit wrong. Config writes the
fixed `Update machine snapshot` subject, commits under the repository's Git
policy, and performs an append-only push. Push failure leaves the local commit
intact.

The repository locator is public configuration. Authentication stays in the
caller's Git environment or credential helper, outside the document, logs, and
Config state.

## Pruning

Pruning crosses two ownership domains without merging them. Mise computes
prunable tool versions and provider-owned packages from its shared inventory.
Config asks mise where its state lives, reads the tracked and trusted
configuration ledgers there to name the links that no longer resolve, and
leaves every deletion to mise. Config discovers package managers from mise
rather than maintaining its own provider list. A provider with no prune
operation is reported and left alone.

Config directly removes only artifacts with verifiable Config provenance. An
undeclared repository hook is eligible when its bytes still match the digest
in Config's adjacent ownership manifest; a changed or non-regular hook and its
record are preserved. A baseline is eligible only after its schema and resource
identity validate and the machine no longer declares that capability. A
completed restore record is eligible only when it validates and belongs to a
managed-checkout identity other than the current one. Pending and current
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
inspection, apply, baselines, repository lifecycle, snapshot, and update flow.
`internal/ui` renders the report, gathers explicit choices, previews cleanup,
and launches scoped operations in child processes. `cmd/config` owns argument
parsing and selects the CLI or terminal interface.

The release contains a single static binary for each supported macOS
architecture. GitHub Releases is Config's distribution channel. Mise hands the
selected release to Config for the initial handoff and for explicit updates;
Config installs that executable as its permanent command. Each release also
ships Config's license and the license material required to redistribute its
dependencies. A read-only canary on a disposable macOS CI runner exercises the
native Finder Favorites boundary without changing the runner's state.
