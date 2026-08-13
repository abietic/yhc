import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

import {
  normalizeOpenSheet,
  responsiveMode,
  sheetProjection,
  shouldCloseSheetOnEscape,
} from '../../internal/webui/assets/layout.mjs';

test('responsive modes use the approved 1180 and 760 pixel boundaries', () => {
  assert.equal(responsiveMode(1180), 'wide');
  assert.equal(responsiveMode(1179), 'medium');
  assert.equal(responsiveMode(760), 'medium');
  assert.equal(responsiveMode(759), 'compact');
});

test('wide mode exposes both persistent regions and closes overlays', () => {
  assert.equal(normalizeOpenSheet(1180, 'inspector'), null);
  assert.deepEqual(sheetProjection(1180, 'navigation'), {
    mode: 'wide',
    openSheetKind: null,
    navigationHidden: false,
    inspectorHidden: false,
    navigationExpanded: false,
    inspectorExpanded: false,
    backdropVisible: false,
    navigationInert: false,
    conversationInert: false,
    inspectorInert: false,
  });
});

test('medium mode keeps navigation persistent and only permits inspector sheet', () => {
  assert.equal(normalizeOpenSheet(900, 'navigation'), null);
  assert.deepEqual(sheetProjection(900, 'inspector'), {
    mode: 'medium',
    openSheetKind: 'inspector',
    navigationHidden: false,
    inspectorHidden: false,
    navigationExpanded: false,
    inspectorExpanded: true,
    backdropVisible: true,
    navigationInert: true,
    conversationInert: true,
    inspectorInert: false,
  });
  assert.equal(sheetProjection(900, null).inspectorHidden, true);
});

test('compact mode projects exactly one accessible sheet', () => {
  const navigation = sheetProjection(700, 'navigation');
  assert.equal(navigation.navigationHidden, false);
  assert.equal(navigation.inspectorHidden, true);
  assert.equal(navigation.navigationExpanded, true);
  assert.equal(navigation.inspectorExpanded, false);
  assert.equal(navigation.backdropVisible, true);

  const inspector = sheetProjection(700, 'inspector');
  assert.equal(inspector.navigationHidden, true);
  assert.equal(inspector.inspectorHidden, false);
  assert.equal(inspector.navigationExpanded, false);
  assert.equal(inspector.inspectorExpanded, true);
  assert.equal(inspector.backdropVisible, true);

  assert.deepEqual(sheetProjection(700, null), {
    mode: 'compact',
    openSheetKind: null,
    navigationHidden: true,
    inspectorHidden: true,
    navigationExpanded: false,
    inspectorExpanded: false,
    backdropVisible: false,
    navigationInert: true,
    conversationInert: false,
    inspectorInert: true,
  });
});

test('unknown sheet kinds fail closed', () => {
  assert.equal(normalizeOpenSheet(700, 'terminal'), null);
});

test('native dialogs own Escape before an open sheet', () => {
  assert.equal(shouldCloseSheetOnEscape('Escape', false, 'navigation'), true);
  assert.equal(shouldCloseSheetOnEscape('Escape', true, 'navigation'), false);
  assert.equal(shouldCloseSheetOnEscape('Enter', false, 'navigation'), false);
  assert.equal(shouldCloseSheetOnEscape('Escape', false, null), false);
});

test('renderer shell exposes labelled sheets and exact responsive geometry', async () => {
  const [html, app, css] = await Promise.all([
    readFile(new URL('../../internal/webui/assets/index.html', import.meta.url), 'utf8'),
    readFile(new URL('../../internal/webui/assets/app.mjs', import.meta.url), 'utf8'),
    readFile(new URL('../../internal/webui/assets/styles.css', import.meta.url), 'utf8'),
  ]);

  for (const id of [
    'navigation',
    'navigation-toggle',
    'navigation-close',
    'conversation',
    'inspector-toggle',
    'inspector-close',
    'sheet-backdrop',
  ]) {
    assert.match(html, new RegExp(`id="${id}"`));
  }
  assert.match(html, /aria-controls="activity-view"/);
  assert.match(html, /aria-controls="review-view"/);
  assert.match(html, /role="tabpanel"/);
  assert.ok(html.indexOf('id="interaction-panel"') < html.indexOf('id="composer"'));

  assert.match(app, /from '\.\/layout\.mjs'/);
  assert.match(app, /function renderSheets\(/);
  assert.match(app, /function openSheet\(/);
  assert.match(app, /function closeSheet\(/);
  assert.match(app, /shouldCloseSheetOnEscape\(event\.key/);
  assert.match(app, /\.inert =/);
  assert.match(app, /shouldCloseSheetOnEscape/);

  assert.match(css, /--canvas:\s*#f7f7f5/);
  assert.match(css, /--canvas:\s*#181916/);
  assert.match(css, /grid-template-columns:\s*244px minmax\(520px, 1fr\) clamp\(320px, 25vw, 380px\)/);
  assert.match(css, /min-width:\s*760px/);
  assert.match(css, /max-width:\s*1179px/);
  assert.match(css, /max-width:\s*759px/);
  assert.match(css, /prefers-reduced-motion:\s*no-preference/);
  assert.doesNotMatch(css, /radial-gradient/);
  assert.doesNotMatch(css, /needs-decision/);
});
