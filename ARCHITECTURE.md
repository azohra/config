# Architecture

Config has one boundary: a caller supplies a Git repository, and Config
reconciles the Mac against the document in that repository. Authentication,
identity recovery, repository selection, and any ceremony required before the
binary starts belong to the caller. The genesis script is one such caller,
published beside Config so a factory-fresh Mac has a verified way to become
one.

## Ownership

The machine repository owns policy and data. Its root `config.toml` is Config's
strict schema 8 contract for native macOS capabilities and snapshot transport.
Earlier schemas and removed fields fail with migration guidance. There is no
legacy parser, declaration compiler or alternate resource engine.

A physical `mise/` directory enables native machine maintenance. Bootstrap and
repair connect it to `~/.config/mise`, replacing only a missing path or an
empty directory. A foreign configuration is a conflict. Those operations
install the verified minimum mise version if needed and preserve a newer
version. Other operations only inspect that prerequisite and direct a missing
setup to repair.

Mise owns machine tools, packages, repositories, dotfiles, services and
defaults. Config presents its aggregate status and native dry-run plans.
Software updates run global tool upgrades without pruning plus native package
upgrades. Repository updates skip dirty checkouts, then apply dotfiles,
including skill links and MCP merges. Repair runs native bootstrap, including
declared hooks and tasks. Config maintains no second inventory for these
resources.

`config update config` resolves Config's own release. A checksummed private
mise adapter in Config's release cache acquires the exact resolved executable
with GitHub asset digest verification. It has no machine configuration.
Downgrade refusal and executable version verification gate atomic installation.
The installed release resumes through a version-bound marker at the canonical
command. `config update` includes the native scopes after this handoff.

CLI and terminal confirmation bind the selected scope, resolved release,
declaration digest and native preview. Effective native includes outside the
managed repository participate in the digest. Authored symlinks bind their
target; effective configuration symlinks also bind their destination's bytes.
Check time is excluded. The child recomputes the plan and refuses changed work,
and native execution checks the declaration digest again. A generation-bound,
cancellable background release check cannot replace a newer scoped preview.
Completed operations invalidate cached availability and retain their result
while native status refreshes. Historical results remain dated and do not show
as new dashboard failures.

Child operations use a typed JSON event stream. Provider bytes remain output
events; the interface handles UTF-8, ANSI and carriage returns across write
boundaries. A self-update emits its installed version, and the parent reopens
the persisted result from that executable.

Config's checkout writes and release installation take an advisory lock and
refuse another writer. Bootstrap releases it before native setup and reacquires
it for retained restoration. Native repair runs without it because a declared
hook can call `config restore`, which takes its own lock. The terminal
interface holds no lock; children take it for the Config work they perform.

An interrupt lands between writes, not inside one. SIGINT and SIGTERM end the
process once no file is mid-write; the staging, rename, and sync of a single
artifact is the section a signal waits out. A write still running after ten
seconds is reported and the process exits anyway.

A signal can still land between two files, and the rest of the design is what
makes that recoverable. A fact the next write destroys is recorded before that write. Config names what
it stages, and the next run sweeps it.

Config owns the managed checkout, the reconciliation model, the `snapshots/`
storage convention, snapshot safety, the terminal interface, and its optional
capabilities. A retained capability is absent unless schema 8 declares it. The
document opts into a capability and supplies personal values or payloads;
Config owns where and how those declarations converge.

## Genesis

`bootstrap.sh` is the pre-Config ceremony served at bootstrap.azohra.com. It
runs in the system Bash 3.2 of a factory-fresh Mac and owns only what must
happen before Config can read a machine repository: the Command Line Tools, a
verified Config release, and one authenticated handoff. It installs nothing
else. After the handoff, Config bootstrap prepares mise when the machine
repository declares native setup.

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

Native setup can invoke `config restore` from mise's `pre-defaults` hook. The
hook must succeed before defaults can reference restored Chrome PWA bundles.
Config bootstrap first runs native setup with `macos-defaults`, `task` and
`final-hook` skipped, then retained restoration, then only those deferred
phases. A failed restore leaves the deferred phases pending for another run.
The pre-defaults hook remains useful for direct native bootstrap and Repair
machine. A machine that clears hardware key mappings declares its `hidutil`
command in that hook before Config restoration. A failure stops that setup
phase; Config has no mapping parser, settings resource or restore checkpoint
for it.

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
bidirectional choice. Snapshot validation checks retained snapshot resources
independently of the native health peer; missing apps or dirty source
repositories do not make unrelated snapshot content unreadable. Normal status
still reports them. Config writes the fixed `Update machine snapshot` subject,
commits under the repository's Git policy, and performs an append-only push.
Push failure leaves the local commit intact.

The repository locator is public configuration. Authentication stays in the
caller's Git environment or credential helper, outside the document, logs, and
Config state.

## Pruning

Prune combines native dry-run cleanup with verified Config artifacts. A
baseline must validate its schema and resource identity and belong to a
disabled capability. A completed restore record must validate and belong to a
different checkout identity; current and pending records stay. A pending marker
is eligible only after its capability is removed, and its bytes must match
Config's marker format.

Mise previews unused tools and configuration records with `prune --dry-run`.
Its package inventory selects configured managers for native package-prune
previews. Unavailable providers leave their state untouched and report a
warning. Config does not infer package ownership or use force flags.

The CLI and terminal interface confirm the exact plan, including native
previews and declaration bytes. Apply recomputes it and refuses changed work,
then checks each Config file's type and digest immediately before deletion.
Repositories, retired skill/MCP/hook ledgers, orphaned dotfile links and old
merged client keys are outside this cleanup plan. Native merge key removal
needs an explicit client migration.

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
