# Architecture

Config has one boundary: a caller supplies a Git repository, and Config
reconciles the Mac against the document in that repository. Authentication,
identity recovery, repository selection, and any ceremony required before the
binary starts belong to the caller. The genesis script is one such caller,
published beside Config so a factory-fresh Mac has a verified way to become
one.

## Ownership

The machine repository owns policy and data. Its root `config.toml` is Config's
strict schema 6 contract for native macOS capabilities and snapshot transport.
Mise configuration belongs to mise: Config does not discover or bind it, install
the machine executable, inspect its inventory, or coordinate upgrades/pruning.
Skills, MCP entries and Git hook placement use native declarations. Config has
no schema or ownership engines for those resources.

`config update` resolves only Config's own release. A checksummed, private mise
adapter in Config's release cache acquires the exact resolved executable with
GitHub asset digest verification. It never selects the machine configuration
or replaces the machine's mise. Downgrade refusal and executable version
verification gate Config's atomic installation. The installed release resumes
through a version-bound marker; old releases' forwarded machine scopes are
accepted only at that handoff and no machine update runs.

The CLI and terminal interface share an update plan whose fingerprint excludes
check time. The interface confirms that fingerprint through a locked child,
which recomputes the release plan and refuses changed work. Applying the plan
acquires its recorded release without repeating discovery. A generation-bound,
cancellable background check cannot replace a newer preview. Completed
operations invalidate cached availability and retain their result while native
status refreshes.

Child operations use a typed JSON event stream. Provider bytes remain output
events; the interface handles UTF-8, ANSI and carriage returns across write
boundaries. A self-update emits its installed version, and the parent reopens
the persisted result from that executable.

Config is one writer at a time. Every command that writes takes an advisory
lock on the managed checkout before it starts, and refuses rather than
interleaving with another Config. The terminal interface holds no lock of its
own, because the commands it launches take it for the work they do.

An interrupt lands between writes, not inside one. SIGINT and SIGTERM end the
process once no file is mid-write; the staging, rename, and sync of a single
artifact is the section a signal waits out. A write still running after ten
seconds is reported and the process exits anyway.

A signal can still land between two files, and the rest of the design is what
makes that recoverable. A fact the next write destroys is recorded before that write. Config names what
it stages, and the next run sweeps it.

Config owns the managed checkout, the reconciliation model, the `snapshots/`
storage convention, snapshot safety, the terminal interface, and its optional
capabilities. A capability is absent unless schema 6 declares it. The document
opts into a capability and supplies personal values or payloads; Config owns
where and how those declarations converge.

## Genesis

`bootstrap.sh` is the pre-Config ceremony served at bootstrap.azohra.com. It
runs in the system Bash 3.2 of a factory-fresh Mac and owns only what must
happen before Config can read a machine repository: the Command Line Tools, a
verified Config release, and one authenticated handoff. It installs nothing
else. Mise, credential managers and other machine tools belong to the native setup
that composes Config's retained restoration commands.

The site serves the committed script unchanged, so it needs no deployment when
Config is released. The script resolves the latest release once, downloads the
archive and `checksums.txt` published with it, verifies the archive, and checks
that the executable reports that version before running it.

The script assumes nothing about where a person keeps their identity. After
verifying Config, it probes the repository with `git ls-remote` in the Mac's
ambient environment, with Git's own credential prompt disabled and the terminal
left available so SSH can confirm a first host key. Keys restored from a backup or
Migration Assistant, a configured credential helper, and a public repository
all succeed here, and Config then takes over with that environment
untouched. The repository itself is
asked for on the terminal when the command carries none. Only when an HTTPS
repository is unreachable does the script ask for a personal access token,
because GitHub accepts no account password for Git over HTTPS. That token is read from the terminal without echo,
written to a mode 0600 file, exposed to system Git exactly once through a
temporary askpass helper that deletes the file as Git reads it, and kept out of
persistent Git configuration by ignoring the system and global files for that
one process. The script refuses to finish if Git did not consume it, and
Config's later reconciliation never sees it. An unreachable SSH repository stops
the script, because nothing it could install would supply a missing key.

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
retry. Independent native capabilities continue after a failure.

`config restore` supports a clean checkout acquired by native setup. It validates
the same root, repository identity, branch and upstream as Config bootstrap,
then establishes a restore identity only when none exists. Pending work resumes;
a completed identity stays complete. Ordinary bootstrap still refuses to infer
a fresh restore from an established checkout without matching progress.

Native setup can invoke `config restore` from mise's `pre-defaults` hook.
The hook must succeed before defaults can reference restored Chrome PWA bundles.
Config itself no longer prepares mise or invokes its defaults phase. Hardware
key mapping remains a small independent native resource using `hidutil`.

Finder Favorites and Chrome PWAs use three-way reconciliation:

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

Each side reduces to the fact Config tracks before it is compared: the PWAs
installed and the ordered path-backed Finder Favorites.
A PWA's name, URL, icon, and schemes are
kept because a restore rebuilds the bundle from them, but Chrome owns that
content and rewrites it on its own schedule, so comparing it would report
Chrome's churn as a choice.

Dock applications belong to native mise declarations. Config does not capture,
compare, or write Dock tiles. Native mise applies the ordered `apps` list,
and any post-defaults restart hook is machine policy.

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

Prune considers only Config-owned artifacts. A baseline must validate its
schema and resource identity and belong to a disabled capability. A completed
restore record must validate and belong to a different checkout identity;
current and pending records stay. A pending marker is eligible only after its
capability is removed, and its bytes must match Config's marker format.

The CLI and terminal interface preview the exact plan. Apply recomputes it and
refuses changed candidates, then checks each file's type and digest immediately
before deletion. Retired skill/MCP/hook ledgers, repositories, tools, packages,
mise registries and downloads are never discovered or removed by this plan.

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
the machine's mise executable and configuration. Config installs the
executable as its permanent command. Each release also
ships Config's license and the license material required to redistribute its
dependencies. A read-only canary on a disposable macOS CI runner exercises the
native Finder Favorites boundary without changing the runner's state.
