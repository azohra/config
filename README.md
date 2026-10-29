# Config

Config keeps a Mac and its Git snapshot in sync. Update apps, tools and
repositories, repair machine setup, and back up Finder Favorites, Chrome PWAs
and application preferences from one terminal interface. Mise runs the native
machine operations.

You choose the machine repository and keep control of its authentication.

## Install

Config requires an Apple Silicon Mac and Git. On a factory-fresh Mac, one
command runs the genesis script that bootstrap.azohra.com serves:

```bash
curl -fsSL bootstrap.azohra.com | bash
```

The script asks for the Git URL of the machine repository, or takes it as an
argument after `bash -s --`. It ensures the Command Line Tools, downloads the
latest Config release, checks the archive against the checksums published with
it, then probes the repository with whatever Git access the Mac already has:
SSH keys restored from a backup or Migration Assistant, a credential helper, or
a public repository. When the probe succeeds, Config takes over with that same
environment. When an HTTPS repository is not reachable, the script asks once
for a personal access token, because GitHub does not accept an account password
for Git over HTTPS. It hands the token to system Git through a temporary
askpass helper that deletes it as Git reads it, and refuses to continue if Git
did not consume it. An SSH repository the Mac cannot reach stops the script.
Nothing else is installed; the machine repository declares every tool.

On a Mac that already has Mise, the released binary can also be run directly:

```bash
mise x github:azohra/config -- \
  config bootstrap https://github.com/owner/machine.git
```

Bootstrap validates the repository, clones it to
`~/Library/Application Support/Config/repository`, installs the released binary
at `~/.local/bin/config`, and restores declared resources and existing
snapshots. When the repository contains `mise/`, bootstrap prepares mise,
installs its declared resources, restores Config snapshots, then applies native
defaults. Each successful Config restore step is checkpointed independently.
Rerunning bootstrap retries native setup and unfinished restoration.

Authentication comes from the calling Git environment or credential helper.
Config rejects repository URLs containing credentials.

## Configure

The machine repository has a strict `config.toml` contract. Each native
capability is optional:

```toml
kind = "azohra.config.machine"
schema = 8
chrome_pwas = true
finder_favorites = true

[repository]
branch = "main"
url = "https://github.com/owner/machine.git"

[[preferences]]
id = "example-app"
name = "Example App"
bundle = "com.example.ExampleApp"
domain = "com.example.ExampleApp"
```

The booleans belong before the first TOML table.

Each preference captures the complete `defaults` domain, unfiltered, into the
repository that Config commits and pushes. Declare a domain only if everything
in it belongs there. Empty domains are refused.

## Native mise setup

Put native mise configuration in the machine repository's `mise/` directory.
Its presence enables machine maintenance in Config. Config connects it to
`~/.config/mise`; an existing nonempty directory or a link to another location
is preserved and reported as a conflict.

Bootstrap and Repair machine install the verified mise 2026.10.21 binary when
mise is missing or older. A newer installed version is retained. Config's own
release transport uses a separate private adapter.

Config asks mise for aggregate machine status and previews its own native
commands before running them. A declared checkout that is behind its remote,
cannot reach it, or has local edits is listed under Machine setup without
failing it; a missing checkout or one with another origin fails it.

| Config action | Native mise commands |
| --- | --- |
| Update software | `upgrade --global --no-prune`, `bootstrap packages upgrade` |
| Update repositories | `bootstrap repos update --skip-dirty`, `dotfiles apply` |
| Repair machine | `bootstrap --skip-dirty` |
| Clean up | `prune`, supported managers' `bootstrap packages prune` |

These operations also work directly through mise. Native declarations own tool
pins, packages, repositories, skill links, MCP merges, Git templates, services
and defaults. Config keeps no second ownership inventory for those resources.
Repository updates skip local edits. Software updates respect declared versions
and leave tool pruning to Clean up.

A native repair hook can restore Config's retained capabilities before defaults
that reference restored applications:

```toml
[bootstrap.hooks.pre-defaults]
run = '"$HOME/.local/bin/config" restore'

[bootstrap.macos.dock]
apps = ["~/Applications/Chrome Apps.localized/Example.app"]
```

`config bootstrap` guarantees this order even without the hook. Full native
repair needs the hook when restoration is still pending. Native commands run
outside Config's checkout lock so the hook can acquire it. Starting or resuming
`config restore` requires a clean, declared `origin/main` checkout. Completed
restores stay complete.

Native Git templates must be installed before repositories are cloned. Mutable
client files need explicit merge or tracking declarations. Removing a source
key from a native merge leaves that key in the target; remove obsolete client
keys explicitly when changing a server's transport or retiring it.

## Migrate to schema 8

Schema 8 is a breaking migration. Config rejects all earlier schemas and removed
fields with a migration error. It does not translate them or run the retired
resource engines.

Prepare the machine repository and the schema-8 Config release together. Keep a
copy of the old executable and declarations until the migration is verified.
A binary update alone does not migrate the repository.

For schema 7, retain the existing native `mise/` declarations and snapshots and
set `schema = 8`. For schema 5 or 6, first transfer `mise`, `agent_skills`,
`mcp_servers`, `repository_hooks` and `[macos]` policy to native declarations,
then remove those fields from `config.toml`. Retain repository identity,
`chrome_pwas`, `finder_favorites` and `[[preferences]]` there. Verify existing
skill links, MCP client state and repository-owned hooks before retiring the
old ownership ledgers outside the snapshot tree.

If hardware key mappings should be cleared, declare that command in native
setup before Config restoration:

```toml
[bootstrap.hooks.pre-defaults]
run = [
  "hidutil property --set '{\"UserKeyMapping\":[]}'",
  '"$HOME/.local/bin/config" restore',
]
```

Install the new release and apply the prepared repository migration before
using machine maintenance. `config --status`, each update scope's `--dry-run`,
and `config prune --dry-run` provide the checks after migration. Config's
snapshot paths and append-only Git transport are unchanged.

## Native state

Captured state has stable paths in the machine repository:

```text
snapshots/chrome-pwas.json
snapshots/chrome-pwas/<id>.icns
snapshots/finder-favorites.json
snapshots/preferences/<preference-id>.plist
```

Finder Favorites and Chrome PWAs are bidirectional. Config compares
the saved snapshot, the live Mac, and a local last-agreement baseline. That
distinguishes a repository edit from a live edit and stops for a choice when
both changed. A declared capability with no snapshot is simply ready for its
first capture.

Finder Favorites own every resolvable path-backed directory entry, including
its label and order. The managed checkout has a portable symbolic target, and
paths in the home directory use `~`. Apply validates every saved directory
before it writes, then adds, moves, renames, and removes entries through
macOS's native shared-file-list API. Pathless, unresolved, and non-directory
sidebar entries remain outside Config's ownership. The complete layout is
verified, and a failed change restores the original.

Chrome PWA comparison tracks installed apps rather than bundle churn that Chrome owns;
replacements are built and signed in staging before live bundles change.

Preferences are one-way backups on an established Mac. Existing backups are
restored only during a pending bootstrap for that exact checkout. A missing
application leaves its step pending without blocking independent resources.

## Use

Run `config` for the terminal interface. It inspects native capabilities,
requires choices for application state, and can save the resulting snapshot.
Inspection is read-only.

```sh
config --status
config path
config restore
config update --dry-run
config update software --yes
config update repositories --yes
config update config --yes
config repair --dry-run
config prune --dry-run
config prune --yes
config --version
```

`config update` selects everything: Config first, then software and repositories.
A release before v0.28.0 that updates itself to this one installs Config alone;
run `config update` again for the machine scopes.
Each scope can run separately. Repair runs the full declared native bootstrap,
including its hooks and tasks.

Update, repair and prune are preview-only without a terminal unless `--yes` is
explicit. Plans bind the declarations and the actions in each native preview;
a changed action requires a new review. Notes such as apps skipped while they
run, versions kept for running processes and release-age warnings are shown but
not bound. Clean up delegates unused tools, configuration records and supported
package cleanup to mise, reports what mise leaves in place, then removes verified
obsolete Config state. It does
not delete repositories or retired skill/MCP/hook ledgers. It does not prune
orphaned dotfile links or remove old MCP keys from client files.

Operations show typed progress and a current provider activity line. Press `d`
for details. Failed operations open the final context, and a self-update
reopens the persisted result from the installed binary. Earlier sessions' results
remain dated under View last operation and do not become current dashboard errors.

Snapshot saves stage the whole managed tree, use `Update machine snapshot`,
honor repository hooks, and push only to the declared branch and upstream.
A rejected push leaves the local commit intact. Config never pulls, rebases or
force-pushes a snapshot.

## Develop

```bash
mise install
mise run check
mise run dist
```

`mise run build` writes `.build/config`. `mise run check` is the same proof CI
runs: formatting, vet, race-enabled tests, vulnerability scanning, module
verification, a redacted secret scan, and the bootstrap script's lint and
behaviour suite. macOS CI also reads Finder Favorites through the native API
without changing them.

`site/` is the Cloudflare Worker behind bootstrap.azohra.com. It serves
`bootstrap.sh` to curl and a page to browsers. A push to main that changes
either deploys it through `mise run deploy`, which needs
`CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` as repository secrets.

For implementation details and trust boundaries, see
[ARCHITECTURE.md](ARCHITECTURE.md).

## License

[MIT](LICENSE)

## Publishing a release

release-drafter keeps one draft release on GitHub. The `skip-changelog` label
excludes a pull request from both release notes and version calculation,
including titles with a `!` marker. `build`, `chore`, `ci`, `docs`, `style`
and `test` titles receive that label automatically; it can also exclude other
changes that do not belong in the installed application's release.

For included changes, labels group features under Added and fixes under Fixed.
`feat` advances the minor version, `fix` advances the patch, and a `!` breaking
marker advances the minor version while below v1. Exclusion is decided first.
Moving to v1 is an explicit decision. Change the breaking version resolver to
`major` in `.github/release-drafter.yml` when making that transition.

The draft records what is unreleased and is where release notes are edited.

Publishing the draft creates the tag. That runs the Release workflow, which is
`mise run release`: goreleaser builds the Apple Silicon archive with the
version from the tag, bundles the licence material, writes `checksums.txt`,
attaches them to the release, and opens a pull request in homebrew-tools with
the generated cask using a token minted from the Bosun app. Assets appear a
minute or two after publishing; if the build fails, the release returns to
draft so the previous one stays latest. Intel Macs are not supported.

`mise run dist` runs the same build as a snapshot on every pull request.
`mise run changelog` renders the history from the shared git-cliff config.

Config downloads releases over HTTPS through mise, which verifies GitHub's asset
digest. It checks the executable's version before installation and refuses a
downgrade. Releases do not require attestations.

