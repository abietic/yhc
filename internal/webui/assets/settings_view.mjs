import {createMCPInventoryView} from './mcp_inventory.mjs';
import {createPermissionSettingsView} from './permission_settings.mjs';

function node(tag, className, text) {
  const item = document.createElement(tag);
  if (className) item.className = className;
  if (text !== undefined) item.textContent = text;
  return item;
}

const groupLabels = Object.freeze({model: 'Model & provider', execution: 'Agent behavior', appearance: 'Appearance & input', other: 'Other settings'});
const groupOrder = ['model', 'execution', 'appearance'];
const modelFieldOrder = ['provider', 'model', 'api_base_url', 'model_profile'];
const groupID = (field) => field.group || 'other';
const groupLabel = (id) => Object.hasOwn(groupLabels, id) ? groupLabels[id] : id;

// Presentation only: fields, values, options and validation still belong to Go.
export function settingsGroups(fields) {
  const groups = new Map();
  for (const entry of Object.entries(fields)) {
    const id = groupID(entry[1]);
    if (!groups.has(id)) groups.set(id, {id, label: groupLabel(id), entries: []});
    groups.get(id).entries.push(entry);
  }
  const rank = (id) => groupOrder.includes(id) ? groupOrder.indexOf(id) : groupOrder.length;
  const fieldRank = (key) => modelFieldOrder.includes(key) ? modelFieldOrder.indexOf(key) : modelFieldOrder.length;
  return [...groups.values()].sort((a, b) => rank(a.id) - rank(b.id) || a.id.localeCompare(b.id))
    .map(group => ({...group, entries: group.entries.sort(([a], [b]) =>
      (group.id === 'model' ? fieldRank(a) - fieldRank(b) : 0) || a.localeCompare(b))}));
}

export function settingsMatches(key, field, category, query) {
  const needle = query.trim().toLowerCase();
  if (!needle && category !== 'all' && groupID(field) !== category) return false;
  return [key, field.label, field.description, groupLabel(groupID(field))].join(' ').toLowerCase().includes(needle);
}

export function settingsCredentialsMatches(category, query) {
  const needle = query.trim().toLowerCase();
  if (!needle) return category === 'model' || category === 'all';
  // Search public help metadata, never credential values or host account data.
  return 'provider credentials api key authentication secure storage host environment'.includes(needle);
}

export function usesSettingChoices(field) {
  return field.choice_only === true || (field.options || []).length > 0 || field.type === 'boolean';
}

// Only selected-scope overrides (including pending resets) can be replaced.
// Go owns the constraint list; inherited effective values are not deletions.
export function settingsSelectionConflicts(key, snapshot, changes) {
  const fields = snapshot.fields;
  const field = fields[key];
  const conflicts = (field?.conflicts_with || []).filter(other => {
    if (!Object.hasOwn(fields, other)) return false;
    const value = Object.hasOwn(changes, other) ? changes[other] : fields[other].stored_value;
    return value !== null && value !== undefined && value !== '';
  });
  return { conflicts, blockers: [...(field?.blocked_by || [])] };
}

export function applyAppearance(snapshot) {
  const fields = snapshot?.fields || {};
  const theme = fields.theme?.effective_value;
  if (theme) document.documentElement.dataset.theme = fields.theme.aliases?.[theme] || theme;
  document.documentElement.dataset.reducedMotion = String(fields.reduced_motion?.effective_value === true);
}

// Form structure and options come from the same Go service as terminal settings.
export function createSettingsView({ api, context, confirmDiscard, confirmModelSelection, onSaved, manageCredentials }) {
  const $ = (id) => document.getElementById(id);
  const dialog = $('settings-dialog');
  let snapshot = null;
  let changes = {};
  let scope = 'user';
  let sessionID;
  let trigger;
  let busy = false;
  let generation = 0;
  let conflicted = false;
  let category = 'model';
  let renderedFields = {};
  let fieldEditors = [];
  let lastSelectionKey;
  const mcpView = createMCPInventoryView({root: $('settings-mcp'), api, context});
  const permissionView = createPermissionSettingsView({root: $('settings-permissions'), actionsRoot: $('settings-permission-actions'), api, context, confirmDiscard, onActivity: updateActions});

  function updateActions() {
    permissionView.setBlocked(busy);
    const panelBusy = busy || permissionView.isBusy();
    const dirty = Object.keys(changes).length > 0;
    $('settings-save').disabled = panelBusy || !dirty || !snapshot || conflicted;
    $('settings-save').textContent = busy ? 'Working…' : 'Save changes';
    $('settings-scope').disabled = panelBusy;
    $('settings-reload').disabled = panelBusy;
    $('settings-close').disabled = panelBusy;
    $('settings-credentials-open').disabled = panelBusy;
    for (const update of fieldEditors) update();
    for (const button of $('settings-categories').querySelectorAll('button')) button.disabled = panelBusy || (!snapshot && !['mcp', 'permissions'].includes(button.dataset.settingsCategory));
    if (!busy && !conflicted && snapshot) $('settings-status').textContent = dirty ? 'Unsaved changes' : 'Up to date';
  }

  function change(key, value) {
    if (JSON.stringify(value) === JSON.stringify(snapshot.fields[key].stored_value)) delete changes[key];
    else changes[key] = value;
    if (Object.hasOwn(changes, key) && value !== null && value !== '' && snapshot.fields[key].conflicts_with?.length) lastSelectionKey = key;
    updateActions();
  }

  function filter() {
    const query = $('settings-search').value.trim().toLowerCase();
    if (query) category = 'all';
    let visible = 0;
    for (const row of $('settings-fields').querySelectorAll('[data-setting]')) {
      row.hidden = !settingsMatches(row.dataset.setting, renderedFields[row.dataset.setting], category, query);
      if (!row.hidden) visible++;
    }
    for (const panel of $('settings-fields').querySelectorAll('[data-settings-group]')) {
      panel.hidden = ![...panel.querySelectorAll('[data-setting]')].some(row => !row.hidden);
    }
    for (const button of $('settings-categories').querySelectorAll('button')) {
      button.setAttribute('aria-pressed', String(button.dataset.settingsCategory === category));
    }
    const credentialsVisible = settingsCredentialsMatches(category, query);
    $('settings-credentials').hidden = !credentialsVisible;
    const mcpVisible = query ? 'mcp servers tools extensions runtime connection status'.includes(query) : category === 'mcp';
    const permissionsVisible = query ? 'permission rules allow deny ask safety local project'.includes(query) : category === 'permissions';
    const specialized = (['mcp', 'permissions'].includes(category) && !query) || (permissionsVisible && visible === 0);
    $('settings-fields').hidden = specialized;
    $('settings-scope-label').hidden = specialized;
    $('settings-save').hidden = specialized;
    $('settings-reload').hidden = specialized;
    $('settings-status').hidden = permissionsVisible && specialized;
    mcpView.setVisible(dialog.open && mcpVisible);
    permissionView.setVisible(dialog.open && permissionsVisible);
    const noResults = $('settings-fields').querySelector('.settings-no-results');
    if (noResults) noResults.hidden = visible > 0 || credentialsVisible || mcpVisible || permissionsVisible;
  }

  function renderNavigation(groups) {
    const navigation = $('settings-categories');
    navigation.replaceChildren();
    for (const group of [{id: 'all', label: 'All settings'}, ...groups, {id: 'permissions', label: 'Permission rules'}, {id: 'mcp', label: 'Extensions · MCP'}]) {
      const button = node('button', 'settings-category', group.label);
      button.type = 'button'; button.dataset.settingsCategory = group.id;
      button.setAttribute('aria-controls', 'settings-body');
      button.onclick = () => {
        category = group.id; $('settings-search').value = ''; filter();
        $('settings-body').scrollTop = 0;
        dialog.scrollTop = 0;
      };
      navigation.append(button);
    }
  }

  function renderFields() {
    const fields = $('settings-fields');
    fields.replaceChildren();
    fieldEditors = [];
    renderedFields = snapshot.fields;
    const groups = settingsGroups(renderedFields);
    if (!['mcp', 'permissions'].includes(category) && !groups.some(group => group.id === category)) category = 'all';
    renderNavigation(groups);
    for (const [index, group] of groups.entries()) {
      const panel = node('section', 'settings-group-panel');
      panel.dataset.settingsGroup = group.id;
      const heading = node('h3', 'settings-group-heading', group.label);
      heading.id = 'settings-group-heading-' + index;
      panel.setAttribute('aria-labelledby', heading.id); panel.append(heading);
      if (group.id === 'model' && renderedFields.model_profile?.conflicts_with?.length) {
        panel.append(node('p', 'setting-description', 'Choose a provider and model, or select a saved model profile. Conflicting overrides are replaced only after confirmation when you save.'));
      }
      for (const [key, field] of group.entries) panel.append(renderField(key, field));
      fields.append(panel);
    }
    const noResults = node('p', 'settings-no-results', 'No settings match your search.');
    noResults.hidden = true; fields.append(noResults); filter(); updateActions();
  }

  function renderField(key, field) {
    const row = node('section', 'setting-row');
    row.dataset.setting = key;
    const info = node('div', 'setting-info');
    const label = node('label', 'setting-label', field.label);
    label.htmlFor = 'setting-' + key;
    info.append(label, node('p', 'setting-description', field.description));
    const provenance = node('p', 'setting-source', 'Effective: ' + String(field.effective_value ?? 'Not set') + ' · From ' + field.source);
    if (field.overridden) provenance.append(node('span', 'setting-overridden', 'Workspace override'));
    info.append(provenance);
    let input;
    const options = field.options || [];
    if (usesSettingChoices(field)) {
      input = node('select');
      if (field.type !== 'boolean') {
        const empty = node('option', '', options.length ? 'Not configured' : 'No saved choices available');
        empty.value = ''; empty.disabled = true; input.append(empty);
        if (field.choice_only === true && !options.length) {
          input.dataset.unavailableChoices = 'true';
          input.title = 'No saved choices are available for this scope.';
        }
      }
      for (const value of field.type === 'boolean' ? [true, false] : options) {
        const opt = node('option', '', value === true ? 'On' : value === false ? 'Off' : value);
        opt.value = String(value); input.append(opt);
      }
      const value = field.stored_value ?? field.effective_value ?? '';
      if (field.type !== 'boolean' && value !== '' && !options.includes(value)) {
        const external = node('option', '', String(value) + ' (configured externally)');
        external.value = value; external.disabled = true; input.prepend(external);
      }
      input.value = String(value);
    } else {
      input = node('input');
      input.type = field.type === 'integer' ? 'number' : 'text';
      if (field.type === 'integer') { input.min = '0'; input.step = '1'; input.required = true; }
      input.value = String(field.stored_value ?? field.effective_value ?? '');
      input.autocomplete = 'off'; input.spellcheck = false;
    }
    input.id = 'setting-' + key;
    input.setAttribute('aria-describedby', 'setting-description-' + key);
    info.querySelector('.setting-description').id = 'setting-description-' + key;
    const initialValue = input.value;
    const hasOverride = field.stored_value !== null && field.stored_value !== undefined;
    let pendingReset = null;
    input.oninput = () => {
      // An empty number is not an intentional unlimited (0) setting.
      if (field.type === 'integer' && input.value === '') { updateActions(); return; }
      change(key, field.type === 'boolean' ? input.value === 'true' : field.type === 'integer' ? Number(input.value) : input.value);
    };
    const controls = node('div', 'setting-controls');
    const reset = node('button', 'setting-reset', scope === 'project' ? 'Use user default' : 'Reset override');
    reset.type = 'button';
    const pending = node('p', 'setting-pending-reset', 'Override removal pending. Save to resolve the inherited value.');
    pending.setAttribute('role', 'status');
    function updateEditor() {
      const edited = input.value !== initialValue || Object.hasOwn(changes, key);
      input.hidden = pendingReset !== null;
      // A queued deletion must not submit or validate the old editor value.
      input.disabled = busy || pendingReset !== null || input.dataset.unavailableChoices === 'true';
      pending.hidden = pendingReset === null;
      reset.textContent = pendingReset ? 'Undo reset' : hasOverride ? (scope === 'project' ? 'Use user default' : 'Reset override') : edited ? 'Discard change' : 'No override';
      reset.disabled = busy || (!pendingReset && !hasOverride && !edited);
    }
    fieldEditors.push(updateEditor);
    reset.onclick = () => {
      if (pendingReset) {
        input.value = pendingReset.value;
        if (pendingReset.hasChange) changes[key] = pendingReset.change;
        else delete changes[key];
        const { start, end, direction } = pendingReset;
        pendingReset = null; updateActions();
        if (!input.disabled) {
          input.focus();
          if (start !== null && start !== undefined) input.setSelectionRange(start, end, direction);
        }
      } else if (hasOverride) {
        // Keep raw DOM input as well as the tracked patch: a blank integer is
        // invalid and deliberately has no new numeric patch value.
        pendingReset = { value: input.value, hasChange: Object.hasOwn(changes, key), change: changes[key], start: input.selectionStart, end: input.selectionEnd, direction: input.selectionDirection };
        change(key, null);
      } else {
        input.value = initialValue;
        delete changes[key]; updateActions();
        if (!input.disabled) input.focus();
      }
    };
    controls.append(input, pending, reset);
    row.append(info, controls); return row;
  }

  async function load() {
    const request = ++generation;
    busy = true; conflicted = false; snapshot = null; changes = {}; lastSelectionKey = undefined;
    renderNavigation([]);
    updateActions(); $('settings-status').textContent = 'Loading shared configuration…';
    try {
      const result = await api('getSettings', { scope, sessionID });
      if (request !== generation) return;
      snapshot = result; renderFields();
    } catch (error) {
      if (request !== generation) return;
      $('settings-fields').replaceChildren(node('p', 'settings-load-error', error.message));
      renderNavigation([]); renderedFields = {}; fieldEditors = [];
      $('settings-status').textContent = 'Could not load settings. Reload to retry.';
    } finally {
      if (request === generation) { busy = false; updateActions(); }
    }
  }

  async function discardAllowed(includePermissions = false) {
    if (permissionView.isBusy()) return false;
    return (Object.keys(changes).length === 0 && (!includePermissions || !permissionView.dirty())) || await confirmDiscard();
  }

  async function close() {
    if (busy || !await discardAllowed(true)) return;
    generation++; changes = {}; lastSelectionKey = undefined; dialog.close(); mcpView.setVisible(false); permissionView.setVisible(false); permissionView.reset(); trigger?.focus();
  }

  // Native form validation must reveal a hidden invalid field before focusing it.
  $('settings-form').addEventListener('invalid', (event) => {
    const row = event.target.closest('[data-setting]');
    if (!row || !snapshot) return;
    event.preventDefault();
    category = groupID(snapshot.fields[row.dataset.setting]);
    $('settings-search').value = ''; filter();
    $('settings-status').textContent = 'Check ' + snapshot.fields[row.dataset.setting].label + ' before saving.';
    event.target.focus();
  }, true);
  $('settings-form').onsubmit = async (event) => {
    event.preventDefault();
    if (busy || permissionView.isBusy() || !snapshot || conflicted || !Object.keys(changes).length) return;
    busy = true; updateActions();
    let selectionFocus;
    try {
      const selectionChanged = key => Object.hasOwn(changes, key) && changes[key] !== null && changes[key] !== '' && snapshot.fields[key]?.conflicts_with?.length;
      const selected = selectionChanged(lastSelectionKey) ? lastSelectionKey : Object.keys(changes).find(selectionChanged);
      let patch = changes;
      if (selected) {
        const { conflicts, blockers } = settingsSelectionConflicts(selected, snapshot, changes);
        if (blockers.length) {
          $('settings-status').textContent = 'Cannot select ' + snapshot.fields[selected].label + ' while ' + blockers.join(', ') + ' is configured in this scope. Update it outside this editor, then reload. No changes were saved.';
          return;
        }
        if (conflicts.length) {
          selectionFocus = selected;
          const confirmed = await confirmModelSelection?.({ scope, selectedLabel: snapshot.fields[selected].label, conflictLabels: conflicts.map(key => snapshot.fields[key].label) });
          if (!confirmed) {
            $('settings-status').textContent = 'Selection replacement cancelled. Your unsaved changes are preserved.';
            return;
          }
          // Do not alter the draft: cancellation, a failed write or 409 keeps it intact.
          patch = { ...changes, ...Object.fromEntries(conflicts.map(key => [key, null])) };
        }
      }
      snapshot = await api('updateSettings', { scope, sessionID, revision: snapshot.revision, changes: patch });
      changes = {}; lastSelectionKey = undefined; renderFields(); onSaved?.(snapshot);
      $('settings-status').textContent = 'Saved. Agent defaults apply to new sessions.';
    } catch (error) {
      conflicted = error.code === 'settings_conflict' || error.code === 'settings_timeout';
      $('settings-status').textContent = error.message;
    } finally {
      busy = false;
      const message = $('settings-status').textContent;
      updateActions(); $('settings-status').textContent = message;
      if (selectionFocus && dialog.open) $('setting-' + selectionFocus)?.focus();
    }
  };
  $('settings-search').oninput = filter;
  $('settings-close').onclick = () => void close();
  dialog.oncancel = (event) => { event.preventDefault(); void close(); };
  $('settings-reload').onclick = async () => { if (await discardAllowed()) await load(); };
  $('settings-scope').onchange = async () => {
    if (!await discardAllowed()) { $('settings-scope').value = scope; return; }
    scope = $('settings-scope').value; await load();
  };
  $('settings-credentials-open').onclick = async () => {
    if (!busy && await discardAllowed(true)) { changes = {}; dialog.close(); mcpView.setVisible(false); permissionView.setVisible(false); permissionView.reset(); manageCredentials(); }
  };

  return {
    async open(source) {
      if (dialog.open) return;
      const current = context();
      sessionID = current.sessionID;
      trigger = source || document.activeElement;
      scope = 'user'; $('settings-scope').value = scope; $('settings-search').value = '';
      category = 'model';
      $('settings-credentials').open = false;
      filter();
      $('settings-scope').querySelector('[value="project"]').disabled = !sessionID;
      $('settings-context').textContent = current.workspace
        ? 'Shared with TUI · Workspace: ' + current.workspace + '. Agent defaults apply to new sessions; permission rules can reload the selected idle session.'
        : 'Shared with TUI · User defaults apply to new sessions. Open a live session to edit workspace overrides.';
      $('settings-credentials-open').hidden = !current.manageCredentials;
      $('settings-credentials-copy').textContent = current.legacyProfile
        ? 'Desktop credentials are encrypted separately. Shared model/provider defaults take priority over the saved fallback; its key is used only for the same provider and endpoint. Close live sessions before replacing credentials.'
        : 'Provider keys stay in the host environment or secure Desktop storage. CLI flags and environment values take precedence over these defaults.';
      dialog.showModal(); $('settings-search').focus(); await load();
    },
    isOpen: () => dialog.open,
    updateContext: () => { mcpView.updateContext(); permissionView.updateContext(); },
  };
}
