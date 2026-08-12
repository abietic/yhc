const titles = Object.freeze({
  turn: 'Turn',
  file_read: 'File read',
  file_search: 'Code search',
  file_change: 'File change',
  command: 'Command',
  network: 'Network request',
  task: 'Task',
  agent: 'Agent',
  tool: 'Tool',
  permission: 'Permission',
  question: 'Question',
  plan_approval: 'Plan approval',
  repeated_tool: 'Repeated tool call',
});

const details = Object.freeze({
  started: 'Started',
  running: 'Running',
  waiting: 'Waiting for your response',
  paused: 'Paused',
  completed: 'Completed',
  stopped: 'Stopped',
  failed: 'Failed',
  resolved: 'Resolved',
});

const statesByKind = Object.freeze({
  turn: new Set(['started', 'waiting', 'completed', 'stopped', 'failed']),
  tool: new Set(['running', 'paused', 'completed', 'stopped', 'failed']),
  task: new Set(['running', 'paused', 'completed', 'stopped', 'failed']),
  agent: new Set(['running', 'paused', 'completed', 'stopped', 'failed']),
  interaction: new Set(['waiting', 'resolved']),
});

const categoriesByKind = Object.freeze({
  turn: new Set(['']),
  tool: new Set([
    'file_read', 'file_search', 'file_change', 'command', 'network',
    'task', 'agent', 'tool',
  ]),
  task: new Set(['task']),
  agent: new Set(['agent']),
  interaction: new Set(['permission', 'question', 'plan_approval', 'repeated_tool']),
});

export function activityPresentation(entry) {
  const kind = typeof entry?.kind === 'string' ? entry.kind : '';
  const state = typeof entry?.state === 'string' ? entry.state : '';
  const category = typeof entry?.category === 'string' ? entry.category : '';
  if (!statesByKind[kind]?.has(state) || !categoriesByKind[kind]?.has(category)) {
    return null;
  }
  const title = kind === 'turn' ? titles.turn : titles[category];
  const detail = kind === 'turn' && state === 'waiting'
    ? 'Waiting for input'
    : details[state];
  return title && detail ? { title, detail } : null;
}
