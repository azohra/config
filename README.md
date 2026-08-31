# Config

Config turns a Git repository into a reproducible Mac setup. Mise handles
tools, packages, repositories, dotfiles, services, and bootstrap tasks. Config
adds a small terminal interface, native macOS resources, safe restore, and
snapshot commits.

You choose the machine repository and keep control of its authentication.

## Install

Config requires macOS, Git, and mise 2026.8.14 at `~/.local/bin/mise`.

```bash
mise x github:azohra/config@0.9.0 -- \
  config bootstrap https://github.com/owner/machine.git
```

Bootstrap validates the repository, clones it to
`~/Library/Application Support/Config/repository`, installs the released binary
at `~/.local/bin/config`, runs mise, and restores any existing snapshots. Each
successful restore step is checkpointed. If one fails, rerun the same command;
completed work will not repeat.

Authentication comes from the calling Git environment or credential helper.
Config rejects repository URLs containing credentials.

## Configure

The machine repository has two inputs:

```text
config.toml       Config's strict machine contract
mise/config.toml  native mise configuration
mise/conf.d/*.toml
```

Only the repository identity is required. Native capabilities are opt-in:

```toml
kind = "azohra.config.machine"
schema = 2
dock = true
chrome_pwas = true
finder_favorites = true

[[repository_hooks]]
name = "post-checkout"
source = "hooks/post-checkout"

[repository]
branch = "main"
url = "https://github.com/owner/machine.git"

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

`[macos]` declares native settings Config converges on every apply. The two
keys opt in differently: `current_host_tap_to_click` is the value Config
keeps, so `false` declares the setting off, while `clear_user_key_mapping`
only means anything when it is `true` — leave it out to let the Mac keep its
own mapping. `[macos.spotlight]` names the shortcut by its symbolic hotkey id
and carries the whole binding, so a Mac bound to different keys is drift.
Config reports a setting it could not read rather than writing over it.

Each `[[preferences]]` entry captures the complete `defaults` domain it names,
byte for byte and unfiltered, into the machine repository — which Config then
commits and pushes. Declare a domain only if everything in it belongs in that
repository. A domain that holds nothing is refused rather than captured.

The booleans belong before the first TOML table. Mise keeps its native syntax:

```toml
min_version = "2026.8.14"

[tools]
node = "24"

[dotfiles]
"~/.gitconfig" = "gitconfig"
```

Config sets `MISE_CONFIG_DIR` for its child processes and stops parent
configuration discovery. It does not create a private mise installation or
inventory. Repository-local mise files can still add narrower authority while
they are loadable, and mise considers that shared inventory when pruning tools
and packages.

## Native state

Captured state has stable paths in the machine repository:

```text
snapshots/dock.apps
snapshots/chrome-pwas.json
snapshots/chrome-pwas/<id>.icns
snapshots/finder-favorites.json
snapshots/preferences/<preference-id>.plist
```

Finder Favorites, the Dock, and Chrome PWAs are bidirectional. Config compares
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

Dock restore changes only application tiles, preserves their full existing
dictionaries, verifies the result, and rolls back a failed write. Chrome PWA
comparison tracks installed apps rather than bundle churn that Chrome owns;
replacements are built and signed in staging before live bundles change.

Preferences are one-way backups on an established Mac. Existing backups are
restored only during a pending bootstrap for that exact checkout. Repository
hooks are one-way desired state: Config refreshes only copies it previously
installed and reports a repository-owned hook as a conflict.

## Use

Run the terminal interface:

```bash
config
```

It inspects first, shows drift and conflicts, applies only selected actions,
then offers to commit and push the resulting repository snapshot. Inspection
is read-only.

Useful non-interactive commands:

```bash
config --status
config path
config update software
config update repositories
config update
config prune --dry-run
config prune --yes
config --version
```

`config update software` updates declared tools and packages without checking
repository remotes. `config update repositories` fast-forwards clean declared
repositories. The unqualified command runs both scopes. A released build first
updates Config itself and continues from the installed binary; a development
build skips that release transition.

`config prune` previews mise's shared inventory decisions alongside Config's
own stale state. Config deletes only artifacts whose ownership it can prove:
unchanged hook copies, baselines for disabled capabilities, and completed
restore records from older managed checkouts. Ambiguous items stay put. A
terminal asks for confirmation; redirected output remains preview-only unless
`--yes` is explicit. The plan is recomputed before the first write.

Snapshot saves stage the whole managed repository, use the fixed commit subject
`Update machine snapshot`, honor repository hooks, and push only to the
declared branch and upstream. A rejected push leaves the local commit intact.

## Develop

```bash
mise install
mise run check
mise run build:release -- v0.0.0-dev
```

`mise run build` writes `.build/config`. `mise run check` is the same proof CI
runs: formatting, vet, race-enabled tests, vulnerability scanning, module
verification, and a redacted secret scan. macOS CI also reads Finder Favorites
through the native API without changing them.

For implementation details and trust boundaries, see
[ARCHITECTURE.md](ARCHITECTURE.md).

## License

[MIT](LICENSE)
