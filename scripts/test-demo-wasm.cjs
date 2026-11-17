// Drives the built browser demo in Node, with no browser: loads the manifest,
// starts the WebAssembly program, sends keys through the same globals the page
// uses, and checks what the terminal would show. Run cmd/demo-wasm/build.sh
// first. Exits non-zero on the first failed check or after 30 seconds.
'use strict';
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

const dir = path.join(__dirname, '..', 'docs', 'landing', 'demo');
const manifest = JSON.parse(fs.readFileSync(path.join(dir, 'manifest.json'), 'utf8'));
for (const name of [manifest.wasm, manifest.exec]) {
  assert.match(name, /^[\w.-]+$/, `manifest names a path, not a file: ${name}`);
}
require(path.join(dir, manifest.exec));

const decoder = new TextDecoder();
let screen = '';
globalThis.firekeeperOutput = data => { screen += decoder.decode(data, { stream: true }); };
// eslint-disable-next-line no-control-regex
const plain = () => screen.replace(/\x1b\[[0-9;?]*[ -/]*[@-~]/g, '');
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

async function waitFor(text, what) {
  for (let i = 0; i < 100; i++) {
    if (plain().includes(text)) return;
    await sleep(50);
  }
  assert.fail(`${what}: never saw ${JSON.stringify(text)}`);
}

async function press(keys, text, what) {
  screen = '';
  globalThis.firekeeperInput(keys);
  await waitFor(text, what);
}

(async () => {
  const timeout = setTimeout(() => { console.error('demo smoke test timed out'); process.exit(1); }, 30000);
  const go = new Go();
  const wasm = fs.readFileSync(path.join(dir, manifest.wasm));
  const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
  const done = go.run(instance);

  assert.equal(typeof globalThis.firekeeperInput, 'function', 'input hook missing');
  await sleep(200);
  globalThis.firekeeperResize(120, 36);
  await waitFor('atlas-studio', 'first frame');
  await press('\x1b[B', 'ember-api', 'arrow down');
  await press('c', 'plan the next release', 'chat view');
  await press('\x1b', 'atlas-studio', 'leave chat');
  await press('?', 'help', 'help overlay');
  await press('x', 'atlas-studio', 'close help');
  await press('\t', 'Copilot', 'overview tab');
  await press('\t', 'Usage', 'usage tab');
  await press('\t', 'atlas-studio', 'back to processes');
  await press('s', 'unavailable in demo', 'terminal switching blocked');
  globalThis.firekeeperPause(true);
  globalThis.firekeeperPause(false);

  globalThis.firekeeperInput('q');
  await done;
  assert.equal(globalThis.firekeeperInput, undefined, 'hooks not released after quit');
  clearTimeout(timeout);
  console.log('demo smoke test passed');
})().catch(error => { console.error(error); process.exit(1); });
