#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
test_root=$(mktemp -d)
trap 'rm -rf "$test_root"' EXIT

make_fixture() {
  local fixture="$test_root/$1"
  mkdir -p "$fixture/dist" "$fixture/scripts" "$test_root/$1-bin"
  cp "$repo_root/mise.toml" "$fixture/mise.toml"
  printf 'git rev-parse HEAD\n' > "$fixture/scripts/release-source.sh"
  printf 'license\n' > "$fixture/LICENSE"
  printf 'dist/\n' > "$fixture/.gitignore"
  printf 'binary\n' > "$fixture/dist/config-darwin-arm64"
  cp "$fixture/dist/config-darwin-arm64" "$fixture/dist/config-darwin-amd64"
  cp "$fixture/LICENSE" "$fixture/dist/LICENSE"
  tar -czf "$fixture/dist/third-party-licenses.tar.gz" -C "$fixture" LICENSE
  printf 'v1.2.3\n' > "$fixture/dist/version.txt"
  (cd "$fixture/dist" && shasum -a 256 LICENSE config-darwin-* third-party-licenses.tar.gz version.txt >checksums.txt)
  git -C "$fixture" init -q -b main
  git -C "$fixture" config user.email test@example.invalid
  git -C "$fixture" config user.name test
  git -C "$fixture" add mise.toml scripts LICENSE .gitignore
  git -C "$fixture" commit -q -m fixture
  git -C "$fixture" tag v1.2.3
  git -C "$fixture" remote add origin https://github.com/azohra/config.git
  cat >"$test_root/$1-bin/gh" <<'GH'
#!/usr/bin/env bash
set -euo pipefail
log=${GH_LOG:?}
printf '%s\n' "$*" >>"$log"
case "$*" in
  "auth status") exit 0 ;;
  "repo view --json nameWithOwner --jq .nameWithOwner") printf '%s\n' 'azohra/config'; exit 0 ;;
  "release view v1.2.3 --json isDraft --jq .isDraft")
    if [[ -f ${GH_STATE:?} ]]; then printf '%s\n' false; exit 0; fi
    if [[ ${GH_SCENARIO:?} == auth-failure ]]; then printf '%s\n' 'HTTP 403: Forbidden' >&2; exit 1; fi
    if [[ ${GH_SCENARIO:?} == existing ]]; then printf '%s\n' true; exit 0; fi
    if [[ ${GH_SCENARIO:?} == published ]]; then printf '%s\n' false; exit 0; fi
    printf '%s\n' 'HTTP 404: release not found' >&2
    exit 1
    ;;
  "release create v1.2.3"*) : >"$GH_STATE"; exit 0 ;;
  "release upload v1.2.3"*) exit 0 ;;
  "release view v1.2.3 --json assets --jq .assets[].name")
    if [[ ${GH_SCENARIO:?} == partial ]]; then printf '%s\n' LICENSE; else printf '%s\n' LICENSE config-darwin-arm64 config-darwin-amd64 third-party-licenses.tar.gz checksums.txt version.txt; fi
    exit 0
    ;;
  "release download v1.2.3 --dir "*) cp dist/* "${@: -1}/"; exit 0 ;;
  "release edit v1.2.3 --draft=false") : >"$GH_STATE"; exit 0 ;;
esac
printf 'unexpected gh invocation: %s\n' "$*" >&2
exit 2
GH
  chmod +x "$test_root/$1-bin/gh"
  printf '%s\n' "$fixture"
}

run_publish() {
  local fixture=$1 scenario=$2
  GITHUB_ACTIONS=true GITHUB_TOKEN=fixture GH_LOG="${fixture}-gh.log" GH_STATE="${fixture}-state" GH_SCENARIO="$scenario" PATH="$test_root/${scenario}-bin:$PATH" \
    mise --cd "$fixture" run release v1.2.3
}

new_release=$(make_fixture new)
run_publish "$new_release" new >/dev/null 2>&1
grep -Eq '^release create v1.2.3 --draft ' "$new_release-gh.log"
grep -Eq '^release edit v1.2.3 --draft=false' "$new_release-gh.log"

existing_release=$(make_fixture existing)
run_publish "$existing_release" existing >/dev/null 2>&1
if grep -Eq '^release create v1.2.3' "$existing_release-gh.log"; then
  echo 'existing draft was recreated' >&2
  exit 1
fi
grep -Eq '^release upload v1.2.3' "$existing_release-gh.log"
grep -Eq '^release edit v1.2.3 --draft=false' "$existing_release-gh.log"

lookup_failure=$(make_fixture auth-failure)
if run_publish "$lookup_failure" auth-failure >"$lookup_failure-output" 2>&1; then
  echo 'release lookup failure unexpectedly succeeded' >&2
  exit 1
fi
grep -Fq 'could not inspect release v1.2.3: HTTP 403: Forbidden' "$lookup_failure-output"
if grep -Eq '^release create v1.2.3' "$lookup_failure-gh.log"; then
  echo 'release lookup failure was masked by create' >&2
  exit 1
fi

published=$(make_fixture published)
if run_publish "$published" published >/dev/null 2>&1; then
  echo 'published release unexpectedly succeeded' >&2
  exit 1
fi
if grep -Eq 'release (upload|create|edit)' "$published-gh.log"; then
  echo 'published release was modified' >&2
  exit 1
fi

partial=$(make_fixture partial)
if run_publish "$partial" partial >/dev/null 2>&1; then
  echo 'partial upload unexpectedly succeeded' >&2
  exit 1
fi
grep -Eq '^release create v1.2.3 --draft ' "$partial-gh.log"
grep -Eq '^release upload v1.2.3' "$partial-gh.log"
if grep -Eq '^release edit v1.2.3 --draft=false' "$partial-gh.log"; then
  echo 'partial upload published the draft' >&2
  exit 1
fi

echo 'release publish contract tests passed'
