# Config

Config turns a Git repository into a reproducible Mac setup. It inspects,
plans, and reconciles resources such as Mise, agent skills, native macOS
settings, Finder Favorites, application state, and the Dock.

You choose the machine repository and keep control of its authentication.

## Install

Config requires macOS and Git. The caller supplies an authenticated repository
handoff. When the machine contract opts into Mise, Config installs its tested
standalone release when that resource first converges.

On a machine that already has Mise, one way to run the released binary is:

```bash
mise x github:azohra/config@0.17.0 -- \
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

Every machine repository has a strict Config contract. A repository that opts
into Mise also carries the native declarations Config connects to Mise's global
configuration:

```text
config.toml       required Config contract
mise/config.toml  optional native Mise configuration
mise/conf.d/*.toml
```

Only the repository identity is required. Every capability is opt-in:

```toml
kind = "azohra.config.machine"
schema = 4
mise = true
dock = true
chrome_pwas = true
finder_favorites = true

[agent_skills]
agents = ["claude-code", "codex"]

[[agent_skills.sources]]
source = "https://github.com/owner/skills.git"
skills = ["orca-cli", "orchestration"]

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
only means anything when it is `true`. Leave it out and the Mac keeps its own
mapping. `[macos.spotlight]` names the shortcut by its symbolic hotkey id
and carries the whole binding, so a Mac bound to different keys is drift.
Config reports a setting it could not read rather than writing over it.

Each `[[preferences]]` entry captures the complete `defaults` domain it names,
byte for byte and unfiltered, into the machine repository, which Config then
commits and pushes. Declare a domain only if everything in it belongs there.
A domain that holds nothing is refused rather than captured.

The booleans belong before the first TOML table. `mise = true` enables the
Mise resource; without it Config neither inspects nor installs Mise. Mise keeps
its native syntax under `mise/`:

```toml
min_version = "2026.9.1"

[tools]
node = "24"

[dotfiles]
"~/.gitconfig" = "gitconfig"
```

Mise is a first-class Config resource. When enabled, its inspection reports the
exact tested version, bootstrap phases, declared tools, and repository
presence. Apply installs the pinned standalone binary at
`~/.local/bin/mise` when necessary, safely connects the repository's `mise/`
directory at `~/.config/mise`, then delegates convergence to `mise bootstrap`.
An absent or empty global directory can be adopted; existing configuration is
reported and left untouched. Config's Mise commands explicitly select that
global directory while native resources and Git operations receive none of
its authority. Repository-local Mise files layer on top in ordinary Mise
precedence, and Mise considers that shared inventory when pruning tools and
packages.

Agent skills are a separate user-wide resource. Global selects user scope, not
exclusive visibility. Every global skill has one
canonical copy under `~/.agents/skills`, which Codex and other universal agents
read directly. `agents` requests targets from the skills CLI; agents with their
own directory, such as Claude Code, receive a link to that canonical copy.
Config deliberately does not encode those paths. Project-local skills remain
part of their own repositories.

Config runs its exact tested `skills@1.5.23` package through `npx` with a
Config-owned npm cache. Node 22.20 or newer and `npx` must already be available;
when Mise is enabled, its tool declaration can provide them before the skill
resource converges. The private cache pins Config's adapter, but the adapter and
an ordinary `npx skills` command intentionally share the same global skill
store. An unreadable or incompatible shared lock makes the resource unavailable
rather than letting the pinned adapter rewrite it. Apply adopts compatible
installs or updates from the declared source without rewriting them. Update and
prune refuse content that has changed since that adoption, and a different
source is always left untouched. The machine repository should not also install
a global `skills` package or run its own reconciler.

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
restored only during a pending bootstrap for that exact checkout. A missing
application leaves its step pending without blocking independent resources.
Repository hooks are one-way desired state: Config refreshes only copies it
previously installed and reports a repository-owned hook as a conflict.

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
config update software --dry-run
config update repositories --yes
config update --yes
config prune --dry-run
config prune --yes
config --version
```

`config update software` previews declared tool, package, and agent-skill
updates without checking repository remotes. `config update repositories`
previews clean declared repositories. The unqualified command covers both
scopes. A terminal asks before running the plan; redirected output remains
preview-only unless `--yes` is explicit. Config shows exact versions where a
provider exposes them and labels checks that can only happen while the provider
runs. A released build updates Config first when necessary and continues from
the installed binary; a development build skips that release transition. When
Mise is not declared, the machine portion has no Mise work to perform.

The terminal interface checks the software scope in the background without
starting the slower repository scan. Selecting software reuses a completed
check or promotes the same in-flight request to the review screen; selecting
another scope cancels it and starts only the requested check. Before applying,
the child command recomputes the selected plan and refuses to continue if its
identity changed.

Operations show typed Config progress and one current provider activity line.
Press `d` to inspect the bounded provider details; failed operations open that
final context automatically. Terminal control sequences and split UTF-8 cannot
change the meaning of a status line. A completed result stays open while
machine status refreshes and records how long the operation took. If Config
replaces itself, the parent interface restarts from the installed binary and
reopens that persisted result. Results remain in Config's private state until
a later operation replaces them.

`config prune` previews Mise's shared inventory decisions alongside Config's
own stale state. A declared but unavailable Mise is reported and its state
stays untouched; Config-owned cleanup still proceeds. Config deletes only
artifacts whose ownership it can prove: unchanged agent-skill placements,
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
