import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const asset = (name) => new URL(
  `../../internal/webui/assets/${name}`,
  import.meta.url,
);

test('desktop and browser bootstrap require protocol v2 end to end', async () => {
  const [main, bootstrapParser, app, transport] = await Promise.all([
    readFile(new URL('../main.cjs', import.meta.url), 'utf8'),
    readFile(new URL('../bootstrap.cjs', import.meta.url), 'utf8'),
    readFile(asset('app.mjs'), 'utf8'),
    readFile(asset('transport.mjs'), 'utf8'),
  ]);

  assert.match(bootstrapParser, /value\.protocol_version !== 2/);
  assert.match(app, /info\.protocolVersion !== 2/);
  assert.match(transport, /session\.protocol_version !== 2/);
  assert.doesNotMatch(bootstrapParser, /value\.protocol_version !== 1/);
  assert.doesNotMatch(app, /info\.protocolVersion !== 1/);
  assert.doesNotMatch(transport, /session\.protocol_version !== 1/);
  assert.match(main, /parseBackendBootstrap\(value, app\.getVersion\(\)\)/);
  assert.match(main, /build:\s*bootstrap\.build/);
  assert.match(app, /buildIdentityViewModel\(info\)/);
  assert.match(app, /buildIdentityNode\.textContent = buildIdentity\.text/);
  assert.match(transport, /return \{ protocolVersion: session\.protocol_version, surface: 'web' \}/);
});

test('the active interaction host is semantic and remains above the composer', async () => {
  const [html, css] = await Promise.all([
    readFile(asset('index.html'), 'utf8'),
    readFile(asset('styles.css'), 'utf8'),
  ]);

  assert.ok(html.indexOf('id="interaction-panel"') < html.indexOf('id="composer"'));
  assert.match(html, /id="interaction-panel"[^>]*aria-labelledby="interaction-title"/s);
  assert.match(html, /id="interaction-body"/);
  assert.match(html, /id="interaction-actions"/);
  assert.match(html, /id="interaction-error"[^>]*role="alert"/s);
  assert.match(html, /id="interaction-queue"/);
  assert.doesNotMatch(html, /id="decision-panel"/);
  assert.match(css, /\.interactions\s*\{/);
  assert.match(css, /max-height: min\(54vh, 620px\)/);
  assert.match(css, /\.interaction-body\s*\{[\s\S]*overflow: auto/);
  assert.match(css, /\.question-option:focus-within/);
  assert.match(css, /\.interaction-actions\s*\{/);
});

test('renderer dispatches by the four tagged kinds and unknown kinds fail closed', async () => {
  const app = await readFile(asset('app.mjs'), 'utf8');

  assert.match(app, /function renderInteraction\(/);
  assert.match(app, /case 'permission':[\s\S]*renderPermissionInteraction/);
  assert.match(app, /case 'question':[\s\S]*renderQuestionInteraction/);
  assert.match(app, /case 'plan_approval':[\s\S]*renderPlanInteraction/);
  assert.match(app, /case 'repeated_tool':[\s\S]*renderRepeatedToolInteraction/);
  assert.match(app, /function renderUnknownInteraction\(/);
  assert.match(app, /Reload session/);
  assert.doesNotMatch(app, /function renderDecision\(|permissionChoices|resolvePermission/);
});

test('question flow is stepped, supports Other, and keeps discussion separate', async () => {
  const app = await readFile(asset('app.mjs'), 'utf8');

  assert.match(app, /function renderQuestionInteraction\(/);
  assert.match(app, /Question \$\{step \+ 1\} of \$\{interaction\.question\.questions\.length\}/);
  assert.match(app, /type = question\.multiSelect \? 'checkbox' : 'radio'/);
  assert.match(app, /editor\.value = ''/);
  assert.match(app, /for \(const control of optionControls\) control\.checked = false/);
  assert.match(app, /Other/);
  assert.match(app, /Back/);
  assert.match(app, /Next/);
  assert.match(app, /Review answers/);
  assert.match(app, /aria-describedby/);
  assert.match(app, /Discuss instead/);
  assert.match(app, /buildQuestionResolution/);
});

test('plan renderer fetches reviewed content and uses the safe Markdown projector', async () => {
  const app = await readFile(asset('app.mjs'), 'utf8');

  assert.match(app, /getInteractionPlan/);
  assert.match(app, /renderMessageContent\(document, 'assistant', review\.content\)/);
  assert.match(app, /buildPlanResolution/);
  assert.match(app, /bypassPermissions/);
  assert.match(app, /requestConfirmation/);
  assert.match(app, /Request changes/);
  assert.match(app, /Cancel plan/);
  assert.match(app, /No valid post-approval mode was provided/);
  assert.doesNotMatch(app, /plan_file_identity|initial_plan_digest|formatDecisionInput/);
});

test('permission and repeated-tool controls come from typed safe variants only', async () => {
  const app = await readFile(asset('app.mjs'), 'utf8');

  assert.match(app, /permission\.grantScopes/);
  assert.match(app, /buildPermissionResolution/);
  assert.match(app, /repeatedTool\.outcomes/);
  assert.match(app, /Continue once/);
  assert.match(app, /Stop and change strategy/);
  assert.match(app, /buildRepeatedToolResolution/);
  assert.doesNotMatch(app, /Formatted decision input|decision-input/);
});
