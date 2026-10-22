import { prioritizeSessionRows } from './provider_setup.mjs';

const text = value => typeof value === 'string' ? value.replace(/[\u0000-\u001f\u007f]/g, '').trim() : '';
const sourceLabel = source => ({ user: 'User defaults', project: 'Project override', default: 'Built-in default' })[source] || 'Runtime-resolved';

// Project only the canonical snapshot's public selection fields. This is not
// another resolver, credential check, import step, or provider connectivity test.
export function homeConfigurationSummary(surface, fields = {}, setup = {}) {
  const profile = text(fields.model_profile?.effective_value);
  const provider = text(fields.provider?.effective_value);
  const model = text(fields.model?.effective_value);
  const entries = [];
  const add = (label, key, value) => entries.push({ label, value, source: sourceLabel(fields[key]?.source) });
  if (profile) add('Model profile', 'model_profile', profile);
  else {
    if (provider) add('Provider', 'provider', provider);
    if (model && (provider || model !== 'default')) add('Model', 'model', model);
  }
  if (entries.length) return {
    title: 'Shared defaults detected', entries,
    detail: 'Shared with TUI. The runtime resolves project overrides, launch flags and credentials when a session opens; this is not a connection check.',
  };
  if (surface === 'web') return {
    title: 'Host-managed configuration', entries,
    detail: 'Credentials are managed by the Desktop App or host process. Review shared defaults here; existing sessions keep their own execution settings.',
  };
  if (setup.configured) return {
    title: 'Desktop fallback saved',
    entries: [
      { label: 'Provider', value: text(setup.provider), source: 'Desktop fallback' },
      { label: 'Model', value: text(setup.model), source: 'Desktop fallback' },
    ].filter(entry => entry.value),
    detail: 'Encrypted credentials are saved separately. Shared defaults and launch configuration take priority; connectivity is checked when you run a task.',
  };
  if (setup.launchReady) return {
    title: 'Launch configuration detected', entries,
    detail: 'The runtime resolves provider and model from the host configuration when a workspace opens. No credentials are shown here.',
  };
  return {
    title: 'Choose your model setup', entries,
    detail: 'A workspace may already have shared configuration. Open it to resolve that setup, or review defaults and add encrypted Desktop credentials.',
  };
}

export function recentWorkbenchSessions(sessions) {
  return prioritizeSessionRows(sessions, { historyExpanded: true }).visible.slice(0, 5);
}
