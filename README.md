# Config

Config reconciles a Mac against a Git repository you control. It gives mise's
machine bootstrap a focused terminal interface, adds native support for a few
macOS resources that can change in both directions, and snapshots accepted
changes back to the same repository.

The caller selects the machine repository and supplies its Git authentication.
Config scopes each mise invocation to that repository's document.

## Install

Config requires macOS, Git, and mise 2026.8.14 or newer installed at
`~/.local/bin/mise`. Mise can run the released binary for the initial handoff:

```bash
mise x github:azohra/config@0.2.2 -- \
  config bootstrap https://github.com/owner/machine.git
```

Authentication comes from the calling environment. `bootstrap` rejects
credential-bearing URLs, clones the selected repository to
`~/Library/Application Support/Config/repository`, validates its contract, and
restores saved state only when that invocation created the clone. An existing
managed checkout follows the normal inspect-and-select workflow.

The initial invocation is one-shot. The machine repository can expose the
declared Config tool through a launcher that scopes its mise shim to this
document; the document itself remains repository state rather than ambient mise
configuration.

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
"github:azohra/config" = "0.2.2"
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

Status, apply, restore, and save include only the capabilities declared here.
The machine document identifies intent; it does not define Config's storage
layout. Config creates captured state under `snapshots/` using stable paths:

```text
snapshots/dock.apps
snapshots/chrome-pwas.json
snapshots/chrome-pwas/<id>.icns
snapshots/preferences/<preference-id>.plist
```

A declared capability with no saved state is uncaptured, not broken. Capture
reads the live Mac and creates its tracked snapshot. Save then commits that
snapshot with the rest of the repository. On a fresh clone, Config restores
only snapshots that exist and leaves uncaptured capabilities ready for their
first capture.

Dock and Chrome PWA snapshots are bidirectional. Config compares the saved
snapshot, the live Mac, and a local last-agreement baseline so it can
distinguish a saved edit from a live edit and stop for a choice when both
changed. It compares which apps are in the Dock and which PWAs are installed,
not what is inside them: Chrome rewrites a PWA bundle whenever a site changes
its icon, and that is Chrome's to change. Preference plists are one-way
backups on an established Mac and are restored only during a fresh bootstrap.

Everything else belongs to mise. Packages, tools, repositories, dotfiles,
services, Compose projects, macOS defaults, LaunchAgents, hooks, and custom
bootstrap tasks keep their native meaning. Config runs `mise bootstrap` to
apply, and reports the status of each bootstrap phase separately so a failure
names the phase it came from.

## Use

```bash
config
```

The interface inspects first, presents uncaptured state, drift, and conflicts,
applies only the selected actions, then offers to save the resulting repository
snapshot.
Inspection is read-only. Apply delegates writes to mise, `defaults`, `hidutil`,
`dockutil`, and the relevant applications.

Useful non-interactive commands:

```bash
config --status
config update
config --version
```

`--status` exits unsuccessfully when a failed check or unresolved bidirectional
choice needs attention. It reports a declared repository that is not checked
out, and one whose checkout holds a different repository. Both answers are
local. It says nothing about how far a checkout has drifted from its remote:
that costs a network round trip each, and `update` already owns it. `update`
is explicit and unscheduled: it updates the standalone mise binary, declared
tools and packages, then fast-forwards clean declared repositories. Dirty
repositories are reported and left untouched.
Declaring `github:azohra/config` makes the initial one-shot invocation
permanent. Changing its selected version upgrades Config through mise.

Saving stages the whole managed repository and creates a commit under the
repository's Git policy. The declared `origin`, branch, and upstream determine
the sole push destination; branch management remains an explicit Git operation.

## Development

```bash
mise install
mise run check
mise run build:release -- v0.0.0-dev
```

`mise run build` writes the development binary to `.build/config`.

`check` runs formatting validation, `go vet`, the race-enabled test suite,
`govulncheck`, module verification, and a redacted secret scan. CI invokes the
same task. Release tags publish bare macOS binaries for Apple silicon and Intel
plus SHA-256 checksums.

The implementation and its trust boundaries are described in
[ARCHITECTURE.md](ARCHITECTURE.md).

## License

[MIT](LICENSE)
