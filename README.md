# Config

Config reconciles a Mac against a Git repository you control. It gives mise's
machine bootstrap a focused terminal interface, adds typed support for
resources that need more than a mise declaration, and snapshots accepted
changes back to the same repository.

The caller selects the machine repository and supplies its Git authentication.
Config scopes each mise invocation to that repository's document.

## Install

Config requires macOS, Git, and mise 2026.8.14 installed at
`~/.local/bin/mise`. Mise can run the released binary for the initial handoff:

```bash
mise x github:azohra/config@0.7.0 -- \
  config bootstrap https://github.com/owner/machine.git
```

Authentication comes from the calling environment. `bootstrap` rejects
credential-bearing URLs, clones the selected repository to
`~/Library/Application Support/Config/repository`, validates its contract, and
records restore progress before it installs the checkout. Bootstrap also copies
the running release to `~/.local/bin/config` before restoration begins. If setup
or one restore step fails, rerun the same bootstrap command to continue the
unfinished steps. A completed checkout, or an existing checkout with no
matching restore record, follows the normal inspect-and-select workflow and is
never restored.

## Machine repository

The repository root contains Config's strict `config.toml` contract. Only the
repository identity is required:

```toml
kind = "azohra.config.machine"
schema = 1

[repository]
branch = "main"
url = "https://github.com/owner/machine.git"
```

Native mise configuration lives separately in `mise/config.toml` and optional
`mise/conf.d/*.toml` fragments:

```toml
min_version = "2026.8.14"

[tools]
node = "24"

[dotfiles]
"~/.gitconfig" = "gitconfig"
```

Only repository identity is required. Config interprets no mise declaration
beyond asking which repository checkouts are declared, so it can report one
that is missing. It selects the repository's `mise/` directory through
`MISE_CONFIG_DIR`, fixes its configuration root to the checkout, and stops
parent discovery. Those values exist only in the child process.

The standalone mise installation is Config's execution substrate, so mise is
excluded from the document's tools and packages.

The repository may opt into Config's native capabilities. Boolean capabilities
are top-level values and therefore appear before the first TOML table:

```toml
kind = "azohra.config.machine"
schema = 1
dock = true
chrome_pwas = true

[[repository_hooks]]
name = "post-checkout"
source = "hooks/post-checkout"

[repository]
branch = "main"
url = "https://github.com/owner/machine.git"

[finder_favorite]
name = "Machine config"

[macos]
current_host_tap_to_click = true
clear_user_key_mapping = true

[macos.spotlight]
id = 64
enabled = false
parameters = [32, 49, 1048576]
type = "standard"

[[preferences]]
id = "example-app"
name = "Example App"
bundle = "com.example.ExampleApp"
domain = "com.example.ExampleApp"
```

Status, apply, restore, and save include only the capabilities declared here.
The machine document identifies intent; it does not define Config's storage
layout. Config creates captured state under `snapshots/` using stable paths:

```text
snapshots/dock.apps
snapshots/chrome-pwas.json
snapshots/chrome-pwas/<id>.icns
snapshots/preferences/<preference-id>.plist
```

A declared snapshot-backed capability with no saved state is uncaptured, not
broken. Capture reads the live Mac and creates its tracked snapshot. Save then
commits that snapshot with the rest of the repository. During a pending restore,
Config restores only snapshots that exist and leaves uncaptured capabilities
ready for their first capture. Authoritative capabilities instead compare their
declared value directly with the Mac and own no snapshot.

Dock and Chrome PWA snapshots are bidirectional. Config compares the saved
snapshot, the live Mac, and a local last-agreement baseline so it can
distinguish a saved edit from a live edit and stop for a choice when both
changed. It compares which apps are in the Dock and which PWAs are installed,
not what is inside them: Chrome rewrites a PWA bundle whenever a site changes
its icon, and that is Chrome's to change. A Dock restore preserves the full
dictionary for every app it keeps and leaves non-app tiles alone. It writes
only `persistent-apps`, verifies the resulting app paths, and restores the
original key if verification fails.

Preference plists are one-way backups on an established Mac and are restored
only while bootstrap has a pending restore for that exact checkout.

Finder Favorites are one-way desired state. Config derives the target from its
managed checkout and the machine document supplies only the label. Inspection
is read-only. Apply uses macOS's native shared-file-list API to make that label
open the managed repository.

Repository hooks are also one-way desired state. Each declaration names a Git
hook and a regular executable source inside the machine repository. Config
installs real copies into its clone template, its managed checkout, and every
repository declared through mise. It refreshes copies it previously installed
when the source changes. Hook names the machine does not declare remain
untouched. A repository-owned hook at the same destination, a linked hooks
directory, or a `core.hooksPath` redirect is reported as a conflict instead of
being overwritten. The template is scoped to Git commands Config runs through
mise; it does not replace repository-owned hook lookup with `core.hooksPath`.

Everything else belongs to mise. Packages, tools, repositories, dotfiles,
services, Compose projects, macOS defaults, LaunchAgents, and custom bootstrap
tasks keep their native meaning. Config runs `mise bootstrap` to apply, and
reports the status of each bootstrap phase separately so a failure names the
phase it came from.

Config isolates which machine document a child mise process loads, but it does
not give mise a private install store or configuration ledger. The machine
repository is this Mac's baseline; a repository-local mise document remains an
additional, narrower authority while its tracked document remains loadable.
Mise combines those tracked documents when it decides whether an installed
tool or managed package is still in use. Config does not reinterpret that
shared inventory or assume the machine document is its only authority.

## Use

```bash
config
```

The interface inspects first, presents uncaptured state, drift, and conflicts,
applies only the selected actions, then offers to save the resulting repository
snapshot.
Inspection is read-only. Apply delegates writes to mise, `defaults`, `hidutil`,
native macOS APIs, and the relevant applications.

Useful non-interactive commands:

```bash
config --status
config path
config update
config prune --dry-run
config --version
```

Config prints the status report whenever it is asked for one and whenever it
is given no terminal, and it exits unsuccessfully either way when a failed
check or unresolved bidirectional choice needs attention. It reports a
declared repository that is not checked out, and one whose checkout holds a
different repository. Both answers are local. It says nothing about how far a
checkout has drifted from its remote:
that costs a network round trip each, and `update` already owns it. `update`
is explicit and unscheduled. After repairing its standalone mise substrate if
needed, a released Config acquires the latest stable, attested release,
atomically installs it as the permanent command, and continues the same update
from that executable. The current release validates the machine document, holds
standalone mise at its tested version, updates declared tools and packages, and
fast-forwards clean declared repositories. Dirty repositories are reported and
left untouched. Unversioned development builds skip Config's release update.
`config path` prints the managed checkout even before it exists.

`config prune` previews stale state across that ownership boundary. Mise names
unused tool versions, package-manager removals, and dead links in its shared
configuration ledger. Config separately names only local artifacts it can
prove it owns: hook copies that still match their ownership manifests,
baselines for disabled capabilities, and completed restore records belonging to
older managed checkouts. A changed hook, unrecognised record, unsupported
package manager, or other ambiguous item is reported and preserved.

In a terminal, the command asks before applying the plan. With redirected
input or output it only previews; `config prune --yes` is the explicit
non-interactive apply form. Apply computes the plan again before the first
write and stops if any candidate changed. `--dry-run` always previews only.

Saving stages the whole managed repository and creates a commit under its Git
policy. Config writes `Update machine snapshot` as the subject; the user only
confirms the save. The declared `origin`, branch, and upstream determine the sole
push destination; branch management remains an explicit Git operation.

## Development

```bash
mise install
mise run check
mise run build:release -- v0.0.0-dev
```

`mise run build` writes the development binary to `.build/config`.

`check` runs formatting validation, `go vet`, the race-enabled test suite,
`govulncheck`, module verification, and a redacted secret scan. CI invokes the
same task. macOS CI also runs `mise run check:darwin-native`, a read-only canary
for the native Finder Favorites integration. Release tags publish bare macOS
binaries for Apple silicon and Intel, Config's license, third-party license
material, and SHA-256 checksums.

The implementation and its trust boundaries are described in
[ARCHITECTURE.md](ARCHITECTURE.md).

## License

[MIT](LICENSE)
