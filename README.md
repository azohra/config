# Config

Config turns a Git repository into a reproducible Mac setup. It inspects,
plans, and reconciles resources such as Mise, agent skills, MCP servers,
native macOS settings, Finder Favorites, application state, and the Dock.

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
schema = 5
mise = true
chrome_pwas = true
finder_favorites = true

[agent_skills]
agents = ["claude-code", "codex"]

[[agent_skills.sources]]
source = "https://github.com/owner/skills.git"
skills = ["orca-cli", "orchestration"]

[mcp_servers]
agents = ["claude-code", "codex"]

[mcp_servers.servers.blender]
command = "uvx"
args = ["blender-mcp"]
env = { BLENDER_HOST = "localhost" }

[mcp_servers.servers.docs]
url = "https://mcp.example.com/mcp"
headers = { X-Region = "us-east-1" }
agents = ["codex"]

[[repository_hooks]]
name = "post-checkout"
source = "hooks/post-checkout"

[repository]
branch = "main"
url = "https://github.com/owner/machine.git"

[macos]
clear_user_key_mapping = true

[[preferences]]
id = "example-app"
name = "Example App"
bundle = "com.example.ExampleApp"
domain = "com.example.ExampleApp"
```

`[macos].clear_user_key_mapping = true` clears hardware key mappings through
`hidutil`. Leave it out and the Mac keeps its own mapping. Config reports a
setting it could not read rather than writing over it.

Current-host tap-to-click and Spotlight shortcuts now belong in native Mise
configuration. Remove `macos.current_host_tap_to_click` and `[macos.spotlight]`
from `config.toml`, enable `mise = true`, and move their values into
`mise/conf.d/macos.toml` using the tested mise 2026.9.11 release:

```toml
[[bootstrap.macos.defaults_entries]]
domain = "NSGlobalDomain"
key = "com.apple.mouse.tapBehavior"
host = "current"
value = 1

[[bootstrap.macos.defaults_entries]]
domain = "com.apple.symbolichotkeys"
key = "AppleSymbolicHotKeys"
path = ["64"]
value = { enabled = false, value = { parameters = [32, 49, 1048576], type = "standard" } }
```

Use `value = 0` for disabled tap-to-click. For a different shortcut, carry its
id into `path` and preserve its enabled state, parameters, and type. The nested
entry replaces only that shortcut and preserves the other symbolic hotkeys.
The removed Config fields are rejected rather than silently ignored.

Each `[[preferences]]` entry captures the complete `defaults` domain it names,
byte for byte and unfiltered, into the machine repository, which Config then
commits and pushes. Declare a domain only if everything in it belongs there.
A domain that holds nothing is refused rather than captured.

The booleans belong before the first TOML table. `mise = true` enables the
Mise resource; without it Config neither inspects nor installs Mise. Mise keeps
its native syntax under `mise/`:

```toml
min_version = "2026.9.11"

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

MCP servers are a third user-wide resource, and one that needs neither Mise
nor Node. The document declares each server once, and Config converges it into
the user-scope configuration of every declared harness: the top-level
`mcpServers` object of `~/.claude.json` for Claude Code, and the
`[mcp_servers.<name>]` tables of `~/.codex/config.toml` for Codex. A stdio
server carries `command`, `args`, and `env`; an http server carries `url` and
`headers`. A server may name its own `agents` to reach fewer harnesses than
the resource default. Config owns the whole entry for a declared name, so a
key the harness added to it, such as a timeout, reads as drift and is
replaced on apply.

Both files are harness state, and Config treats them that way. It never
creates either one: a harness that has not run yet is reported as not present
and converges on the first run after its file exists. Config rewrites only the
declared entries and leaves every other key, table, comment, and undeclared
server byte for byte. It writes only when a declared entry actually differs.
A harness file that is a link into the managed checkout, which is how
a dotfile declaration usually places `~/.codex/config.toml`, is written
through the link so the next snapshot records the result. A link that points
anywhere else is reported and left alone. A running harness may still rewrite
its file from memory after Config does; the next `config` run converges it
again. Config records which names it wrote, so `config prune` removes an entry
only when Config wrote it and it still reads as written.

## Dock applications

Declare pinned applications in native mise configuration:

```toml
[bootstrap.macos.dock]
apps = [
  "/System/Applications/Utilities/Terminal.app",
  "/Applications/Firefox.app",
]
```

Edit the list to add, remove, or reorder pins, then apply the Mise resource.
Mise compares application identity and order, preserves existing tile metadata,
and leaves non-application tiles alone. Dragging icons in Dock does not update
this declaration; the next apply restores the declared order. An empty list
removes application pins, while omitting `apps` leaves them unmanaged.

Mise does not restart Dock itself. Relaunch it with `killall Dock`, or declare a
`[bootstrap.hooks.post-defaults]` hook. Hooks run even when defaults already match.

Schema 5 removes Config's `dock` toggle and Dock snapshot resource. To migrate
from schema 4, copy the paths from `snapshots/dock.apps` into this `apps` list,
remove `dock` from `config.toml`, and set `schema = 5`. After verifying the layout,
remove the retired snapshot. Config's prune can reclaim the old local Dock
baseline and restart marker. Machines without mise can leave Dock unmanaged.

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
artifacts whose ownership it can prove: unchanged agent-skill placements, MCP
server entries it wrote and no longer declares, unchanged hook copies,
baselines for disabled capabilities, and completed
restore records from older managed checkouts. It also reclaims the Homebrew
installers Mise leaves cached for thirty days, reporting the bytes each holds
first. Ambiguous items stay put. A terminal asks for
confirmation; redirected output remains preview-only unless `--yes` is
explicit. The plan is recomputed before the first write.

Snapshot saves stage the whole managed repository, use the fixed commit subject
`Update machine snapshot`, honor repository hooks, and push only to the
declared branch and upstream. A rejected push leaves the local commit intact.

## Develop

```bash
mise install
mise run check
mise run build:dist
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

