import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const asset = (name) => new URL(
  `../../internal/webui/assets/${name}`,
  import.meta.url,
);

test('composer keeps draft admission separate and exposes explicit rebind', async () => {
  const [app, html, css] = await Promise.all([
    readFile(asset('app.mjs'), 'utf8'),
    readFile(asset('index.html'), 'utf8'),
    readFile(asset('styles.css'), 'utf8'),
  ]);

  assert.match(html, /id="model-remediation"/);
  assert.match(html, /id="model-rebind"[^>]*type="button"/s);
  assert.match(html, />Rebind current model</);
  assert.match(app, /canEditDraft/);
  assert.match(app, /modelRebindSelector/);
  assert.match(app, /\$\('prompt'\)\.disabled = !canEditDraft\(current\)/);
  assert.match(app, /\$\('send'\)\.disabled = !canSubmitTurn\(current\)/);
  assert.doesNotMatch(app, /\$\('prompt'\)\.disabled = !canSubmitTurn/);
  assert.match(
    app,
    /\$\('model-rebind'\)\.onclick[\s\S]*?field: 'model',[\s\S]*?value: selector/,
  );
  assert.match(css, /\.model-remediation\s*\{/);
});
