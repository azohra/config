# Config

Config restores and backs up native Mac state from a Git repository: Finder
Favorites, Chrome PWAs and application preferences.
Mise owns tools, packages, repositories, dotfiles, services and their lifecycle.

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
snapshots. Each successful restore step is checkpointed independently. If one
fails, rerun the same command; completed work will not repeat.

Authentication comes from the calling Git environment or credential helper.
Config rejects repository URLs containing credentials.

## Configure

The machine repository has a strict `config.toml` contract. Each native
capability is optional:

```toml
kind = "azohra.config.machine"
schema = 7
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

Config does not install or select the machine's mise, parse its declarations,
or wrap its status, upgrade and prune commands. Run those commands directly:

```sh
mise bootstrap status
mise bootstrap
mise outdated --global
mise upgrade --global
mise bootstrap packages upgrade
mise bootstrap repos update
mise prune --dry-run
```

With mise 2026.10.21 or newer, a repository can retain its native configuration
under `mise/`. After verified mise acquisition, native bootstrap can clone it
to Config's managed location and select that nested directory:

```sh
mise bootstrap --from https://github.com/owner/machine.git \
  --from-dir "$HOME/Library/Application Support/Config/repository" \
  --config-dir mise
```

Install Config through its verified release path before its restoration hook
runs. In the native mise declarations, restore Config's retained capabilities
before applying defaults that depend on them:

```toml
[bootstrap.hooks.pre-defaults]
run = 'config restore'

[bootstrap.macos.dock]
apps = ["~/Applications/Chrome Apps.localized/Example.app"]
```

Starting or resuming `config restore` requires a clean, declared `origin/main`
checkout. A completed restore remains complete, including after later local
configuration changes. A failed restoration stops the hook before Dock defaults. Git
repository acquisition, credentials, package installation and shell activation
remain the native setup's responsibility. The genesis script remains a way to
acquire Config and restore its capabilities; it does not install mise.

Native Git templates must be installed before repositories are cloned. Native
MCP fragments and source-backed skill links belong in mise's dotfiles and
repository declarations. Mutable client settings require explicit merge or
tracking semantics; linking a file does not make all its runtime state safe to
commit.

## Migrate from schema 6

Install the schema-7 Config release before changing `config.toml`. Set
`schema = 7` and remove the `[macos]` table. If `clear_user_key_mapping` was
enabled, put its macOS command before Config restoration in the native hook:

```toml
[bootstrap.hooks.pre-defaults]
run = [
  "hidutil property --set '{\"UserKeyMapping\":[]}'",
  'config restore',
]
```

The native hook clears mappings each time full bootstrap or its
`--only macos-defaults` phase runs. A failing command stops the phase before
restoration and defaults. Config no longer inspects key mappings or offers a
macOS settings resource. Snapshot validation still checks the saved native
resources and unresolved choices.

## Migrate from schema 5

Remove `mise`, `agent_skills`, `mcp_servers` and `repository_hooks` from
`config.toml` and set `schema = 7`. These fields are rejected, not ignored.
Apply the schema-6 migration above as well.
Before removing them, transfer their declarations to native mise and verify
existing placements as well as cold clones. Preserve client state and unrelated
skills and repository-owned hooks. Archive the retired ownership ledgers and
accepted hook copies outside the snapshot tree; Config no longer reads or
prunes them.

`config update` now updates Config only. The `software` and `repositories`
selectors are removed. Older Config releases may forward one of those
selectors during their version-bound install handoff; that handoff completes
without running the removed machine updater. Use native mise for subsequent
machine operations. Config's private, checksummed release transport is separate
from the machine's mise installation.

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
config update --yes
config prune --dry-run
config prune --yes
config --version
```

Update and prune are preview-only without a terminal unless `--yes` is
explicit. Their plans are recomputed before mutation; a changed plan requires
a new review. Prune removes only verified Config baselines, completed restore
records from older checkouts, and markers for capabilities no longer declared.
Mise and retired skill/MCP/hook state remain outside that plan.

Operations show typed progress and a current provider activity line. Press `d`
for details. Failed operations open the final context, and a self-update
reopens the persisted result from the installed binary.

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

