// Runs bootstrap.sh against stubbed tools. There is no terminal, so the token
// path stops at its terminal requirement and the askpass helper is proven
// alone.

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

const script = new URL('../bootstrap.sh', import.meta.url);
const source = readFileSync(script, 'utf8');
const archiveName = 'config_darwin_arm64.tar.gz';

const genesis = (t, { repository, reachable = true, corrupt = false }) => {
  const args = repository === undefined ? [] : [repository];
  const root = mkdtempSync(join(tmpdir(), 'bootstrap-genesis.'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const bin = join(root, 'bin');
  const log = join(root, 'log');
  mkdirSync(bin);
  const executable = (file, body) => {
    writeFileSync(file, `#!/bin/sh\n${body}\n`);
    chmodSync(file, 0o755);
  };

  const packageRoot = join(root, 'package');
  mkdirSync(packageRoot);
  executable(join(packageRoot, 'config'), [
    `printf 'config %s\\n' "$*" >> "${log}"`,
    '[ "$1" != --version ] || { echo "config v1.2.3"; exit 0; }',
    `printf 'askpass=%s global=%s prompt=%s\\n' "\${GIT_ASKPASS:-}" "\${GIT_CONFIG_GLOBAL:-}" "\${GIT_TERMINAL_PROMPT:-}" >> "${log}"`,
  ].join('\n'));
  const archive = join(root, archiveName);
  const packed = spawnSync('tar', ['-czf', archive, '-C', packageRoot, 'config'], { encoding: 'utf8' });
  assert.equal(packed.status, 0, packed.stderr);
  const digest = createHash('sha256').update(readFileSync(archive)).digest('hex');
  const checksums = join(root, 'checksums.txt');
  writeFileSync(checksums, `${corrupt ? '0'.repeat(64) : digest}  ${archiveName}\n`);

  executable(join(bin, 'uname'), 'case $1 in -s) echo Darwin ;; -m) echo arm64 ;; esac');
  executable(join(bin, 'xcode-select'), 'exit 0');
  executable(join(bin, 'curl'), [
    `printf 'curl %s\\n' "$*" >> "${log}"`,
    'out=; for arg in "$@"; do case $arg in *releases/latest) echo https://github.com/azohra/config/releases/tag/v1.2.3; exit 0 ;; esac; done',
    'while [ $# -gt 0 ]; do [ "$1" = -o ] && out=$2; url=$1; shift; done',
    `case $url in *checksums.txt) cp "${checksums}" "$out" ;; *) cp "${archive}" "$out" ;; esac`,
  ].join('\n'));
  executable(join(bin, 'git'), [
    `printf 'git %s\\n' "$*" >> "${log}"`,
    `[ "${reachable}" = true ] || { echo 'fatal: could not read from remote' >&2; exit 128; }`,
  ].join('\n'));

  const result = spawnSync('bash', [script.pathname, ...args], {
    encoding: 'utf8',
    env: { PATH: `${bin}:${process.env.PATH}`, TMPDIR: root, HOME: root, TERM: 'dumb' },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  return { ...result, log: existsSync(log) ? readFileSync(log, 'utf8') : '' };
};

test('a reachable repository hands off with the ambient Git environment', (t) => {
  const run = genesis(t, { repository: 'https://github.com/owner/machine.git' });
  assert.equal(run.status, 0, run.stderr);
  assert.match(run.log, /curl .*releases\/download\/v1\.2\.3\/config_darwin_arm64\.tar\.gz/);
  assert.match(run.log, /curl .*releases\/download\/v1\.2\.3\/checksums\.txt/);
  assert.match(run.log, /git ls-remote --exit-code -- https:\/\/github\.com\/owner\/machine\.git HEAD/);
  assert.match(run.log, /config bootstrap https:\/\/github\.com\/owner\/machine\.git/);
  assert.match(run.log, /askpass= global= prompt=$/m);
  assert.match(run.stdout, /Config v1\.2\.3/);
  assert.match(run.stdout, /Repository reachable/);
});

test('an unreachable SSH repository stops before any credential prompt', (t) => {
  const run = genesis(t, { repository: 'git@github.com:owner/machine.git', reachable: false });
  assert.notEqual(run.status, 0);
  assert.match(run.stderr, /could not read from remote/);
  assert.match(run.stderr, /not reachable with this Mac's SSH configuration/);
  assert.doesNotMatch(run.log, /config bootstrap/);
});

test('an unreachable HTTPS repository needs a terminal for the token', (t) => {
  const run = genesis(t, { repository: 'https://github.com/owner/machine.git', reachable: false });
  assert.notEqual(run.status, 0);
  assert.match(run.stderr, /No terminal is available to enter a personal access token/);
  assert.doesNotMatch(run.log, /config bootstrap/);
});

test('without an argument the repository is asked for on the terminal', (t) => {
  const run = genesis(t, {});
  assert.notEqual(run.status, 0);
  assert.match(run.stderr, /No terminal is available to enter the machine repository/);
  assert.doesNotMatch(run.log, /curl/);
});

test('a locator that is neither HTTPS nor SSH is refused before any download', (t) => {
  const run = genesis(t, { repository: '/Users/me/machine' });
  assert.notEqual(run.status, 0);
  assert.match(run.stderr, /must be an HTTPS or SSH Git URL/);
  assert.doesNotMatch(run.log, /curl/);
});

test('an archive that does not match the published checksums is refused', (t) => {
  const run = genesis(t, { repository: 'https://github.com/owner/machine.git', corrupt: true });
  assert.notEqual(run.status, 0);
  assert.match(run.stderr, /Config checksum verification failed/);
  assert.doesNotMatch(run.log, /config bootstrap/);
});

test('the Git askpass consumes the credential exactly once', (t) => {
  const body = source.match(/cat >"\$git_askpass" <<'EOF'\n([\s\S]*?)\nEOF/);
  assert.ok(body);

  const root = mkdtempSync(join(tmpdir(), 'bootstrap-askpass.'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const askpass = join(root, 'askpass');
  const credential = join(root, 'credential');
  writeFileSync(askpass, `${body[1]}\n`);
  chmodSync(askpass, 0o700);
  writeFileSync(credential, 'test-credential\n', { mode: 0o600 });

  const env = { ...process.env, CONFIG_GIT_CREDENTIAL_FILE: credential };
  const username = spawnSync(askpass, ['Username for https://github.com'], { env, encoding: 'utf8' });
  assert.equal(username.stdout, 'x-access-token\n');

  const password = spawnSync(askpass, ['Password for https://github.com'], { env, encoding: 'utf8' });
  assert.equal(password.stdout, 'test-credential\n');
  assert.equal(existsSync(credential), false);

  const reuse = spawnSync(askpass, ['Password for https://github.com'], { env, encoding: 'utf8' });
  assert.notEqual(reuse.status, 0);
  assert.equal(reuse.stdout, '');
});
