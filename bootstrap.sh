#!/usr/bin/env bash
# Bootstrap a fresh Mac from nothing.
#   curl -fsSL bootstrap.azohra.com | bash -s -- https://github.com/owner/machine.git
#
# Piping ignores the shebang, so this deliberately stays within the system Bash
# 3.2 available on a factory-fresh Mac. Every byte served here is public.
set -euo pipefail

[[ $# -eq 1 && -n $1 ]] || {
    printf 'error: usage: curl -fsSL bootstrap.azohra.com | bash -s -- <configuration-repository>\n' >&2
    exit 2
}
CONFIG_REPOSITORY=$1
CONFIG_RELEASES=https://github.com/azohra/config/releases

bold=''
muted=''
yellow=''
green=''
red=''
reset=''
if [[ -t 1 && ${TERM:-dumb} != dumb && -z ${NO_COLOR:-} ]]; then
    bold=$'\033[1m'
    muted=$'\033[38;5;245m'
    yellow=$'\033[33m'
    green=$'\033[32m'
    red=$'\033[31m'
    reset=$'\033[0m'
fi

banner() {
    printf '\n%sCONFIG%s %s/ GENESIS%s\n' "$bold" "$reset" "$muted" "$reset"
    printf '%sMake this Mac yours.%s\n' "$yellow" "$reset"
}
step() { printf '\n%s→%s %s\n' "$yellow" "$reset" "$1"; }
ready() { printf '%s✓%s %s\n' "$green" "$reset" "$1"; }
die() {
    printf '%serror:%s %s\n' "$red" "$reset" "$1" >&2
    exit 1
}

# Config validates the locator fully before it clones. Genesis only needs to
# know whether a token can stand in for missing authentication, and that is
# true of HTTPS alone.
case $CONFIG_REPOSITORY in
    https://*) https_repository=1 ;;
    ssh://* | *@*:*) https_repository=0 ;;
    *) die "The configuration repository must be an HTTPS or SSH Git URL." ;;
esac

[[ $(uname -s) == Darwin ]] || die "This script is for macOS."
[[ $(uname -m) == arm64 ]] || die "Config supports Apple Silicon Macs only."

banner

if xcode-select -p >/dev/null 2>&1; then
    ready "Xcode tools"
else
    step "Install Xcode tools (accept the macOS prompt)"
    xcode-select --install || true
    until xcode-select -p >/dev/null 2>&1; do sleep 5; done
    ready "Xcode tools"
fi

genesis_root=$(mktemp -d "${TMPDIR:-/tmp}/config-genesis.XXXXXX")
trap 'rm -rf "$genesis_root"' EXIT

# The latest release is resolved once so the archive, its checksums, and the
# version check all describe the same release.
step "Download the latest Config release"
release_url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$CONFIG_RELEASES/latest") ||
    die "The latest Config release could not be resolved."
config_version=${release_url##*/}
[[ $config_version == v[0-9]* ]] || die "Config release $config_version is not a version."
config_asset=config_darwin_arm64.tar.gz
config_archive="$genesis_root/$config_asset"
curl -fsSL "$CONFIG_RELEASES/download/$config_version/$config_asset" -o "$config_archive"
curl -fsSL "$CONFIG_RELEASES/download/$config_version/checksums.txt" -o "$genesis_root/checksums.txt"
(cd "$genesis_root" && grep " $config_asset\$" checksums.txt | shasum -a 256 -c - >/dev/null) ||
    die "Config checksum verification failed."
config_bin="$genesis_root/config"
tar -xzf "$config_archive" -C "$genesis_root" config || die "Config archive extraction failed."
chmod 0755 "$config_bin"
[[ $("$config_bin" --version 2>/dev/null) == "config $config_version" ]] ||
    die "Config version verification failed."
ready "Config $config_version"

# Whatever authentication this Mac already has is the genesis: keys restored
# from a backup or Migration Assistant, a credential helper, or a public
# repository. The probe runs on this Mac with that ambient environment, asks Git
# for nothing, and leaves SSH free to confirm a first host key on the terminal.
step "Reach the configuration repository"
tty_input=/dev/null
if (: </dev/tty) 2>/dev/null; then
    tty_input=/dev/tty
fi
probe_output="$genesis_root/probe"
if GIT_TERMINAL_PROMPT=0 \
    git ls-remote --exit-code -- "$CONFIG_REPOSITORY" HEAD \
    >/dev/null 2>"$probe_output" <"$tty_input"; then
    ready "Repository reachable"

    step "Hand off the configuration repository"
    "$config_bin" bootstrap "$CONFIG_REPOSITORY"
    exit 0
fi

[[ $https_repository -eq 1 ]] || {
    cat "$probe_output" >&2
    die "The configuration repository is not reachable with this Mac's SSH configuration."
}
[[ $tty_input == /dev/tty ]] || die "No terminal is available to enter a credential."

# One token, entered once, exposed to system Git once through a temporary
# askpass helper, deleted as Git reads it, and never handed to Config's later
# reconciliation processes. Persistent Git configuration is ignored so the
# token is neither read from nor written to a credential helper.
repository_host=${CONFIG_REPOSITORY#https://}
repository_host=${repository_host%%/*}
step "Enter a personal access token for $repository_host"
printf 'Paste the token, then press Enter: ' >/dev/tty
IFS= read -r -s git_credential </dev/tty
printf '\n' >/dev/tty
[[ -n $git_credential ]] || die "The personal access token is empty."

git_credential_file="$genesis_root/git-credential"
(umask 077 && printf '%s\n' "$git_credential" >"$git_credential_file")
unset git_credential GH_TOKEN GITHUB_TOKEN

git_askpass="$genesis_root/git-askpass"
cat >"$git_askpass" <<'EOF'
#!/bin/sh
case ${1:-} in
    *Username*) printf '%s\n' x-access-token ;;
    *Password*)
        cat "$CONFIG_GIT_CREDENTIAL_FILE" || exit 1
        rm -f "$CONFIG_GIT_CREDENTIAL_FILE"
        ;;
    *) exit 1 ;;
esac
EOF
chmod 0700 "$git_askpass"

git_config="$genesis_root/gitconfig"
printf '[credential]\n\thelper =\n' >"$git_config"
ready "Personal access token"

step "Hand off the configuration repository"
LC_ALL=C \
    GIT_CONFIG_NOSYSTEM=1 \
    GIT_CONFIG_GLOBAL="$git_config" \
    GIT_ASKPASS="$git_askpass" \
    GIT_TERMINAL_PROMPT=0 \
    CONFIG_GIT_CREDENTIAL_FILE="$git_credential_file" \
    "$config_bin" bootstrap "$CONFIG_REPOSITORY"
[[ ! -e $git_credential_file ]] ||
    die "Git did not consume the temporary personal access token."
