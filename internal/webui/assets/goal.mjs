const MAX_U64 = 18446744073709551615n;
const decimal = value => typeof value === 'string' &&
  /^(0|[1-9][0-9]*)$/.test(value) && value.length <= 20 && BigInt(value) <= MAX_U64;
const positive = value => decimal(value) && value !== '0';
const operations = new Set(['create', 'edit', 'pause', 'resume', 'budget', 'clear', 'continue']);
const statuses = new Set(['active', 'paused', 'blocked', 'budget_limited', 'usage_limited', 'complete']);
const validText = value => typeof value === 'string' && value.isWellFormed() &&
  !value.includes('\0') && [...value].length <= 4000;
const validID = value => typeof value === 'string' && value.isWellFormed() &&
  !value.includes('\0') && [...value].length <= 256;
const staleNotice = 'Goal 已变更；草稿版本未更新。请确认丢弃后重新读取。';
const foreignNotice = '会话已切换；旧 Goal 草稿未发送。请确认丢弃后重新读取。';

export function normalizeGoalState(value, sessionID) {
  const fields = ['session_id', 'available', 'diagnostic', 'default_token_budget', 'goal'];
  const goalFields = ['goal_id', 'objective', 'status', 'revision', 'token_budget',
    'tokens_used', 'root_active_time_ms', 'usage_coverage', 'status_reason_code', 'status_reason'];
  const goal = value?.goal;
  const badGoal = goal !== null && (!goal || typeof goal !== 'object' || Array.isArray(goal) ||
    Object.keys(goal).some(key => !goalFields.includes(key)) ||
    !goalFields.filter(key => key !== 'token_budget').every(key => typeof goal[key] === 'string') ||
    !validID(goal.goal_id) || !goal.goal_id || !validText(goal.objective) ||
    !statuses.has(goal.status) || !positive(goal.revision) || !decimal(goal.tokens_used) ||
    !decimal(goal.root_active_time_ms) || (goal.token_budget !== null && !positive(goal.token_budget)));
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
    Object.keys(value).some(key => !fields.includes(key)) || value.session_id !== sessionID ||
    typeof value.available !== 'boolean' || typeof value.diagnostic !== 'string' ||
    (value.default_token_budget !== null && !positive(value.default_token_budget)) || badGoal) {
    throw new TypeError('Invalid Goal state');
  }
  return value;
}

export function createGoalController({ api, onChange = () => {}, onStarted = () => {} }) {
  let context = {}, epoch = 0, contextEpoch = 0;
  let value = { snapshot: null, status: 'idle', notice: '', draft: null };
  const publish = patch => { value = { ...value, ...patch }; onChange(value); };
  const usable = () => context.live === true && context.backendReady === true &&
    context.parentView !== false && typeof context.sessionID === 'string';
  const ready = () => usable() && value.status === 'ready' && value.snapshot?.available === true;
  const expected = () => ({ expected_goal_id: value.snapshot?.goal?.goal_id || '',
    expected_revision: value.snapshot?.goal?.revision || '0' });
  const foreign = () => value.draft && value.draft.sessionID !== context.sessionID;
  const stale = () => value.draft && (foreign() ||
    value.draft.goalID !== expected().expected_goal_id ||
    value.draft.revision !== expected().expected_revision);
  const allowed = operation => {
    const status = value.snapshot?.goal?.status;
    if (operation === 'create') return !status;
    if (operation === 'clear') return Boolean(status);
    if (status === 'complete') return false;
    if (['paused', 'blocked', 'budget_limited'].includes(status)) return ['edit', 'budget', 'resume'].includes(operation);
    if (status === 'usage_limited') return ['edit', 'budget'].includes(operation);
    return status === 'active' && ['continue', 'pause', 'edit', 'budget'].includes(operation);
  };
  const canControl = operation => {
    if (!operations.has(operation) || !ready() || context.activeTurnID || stale() || !allowed(operation)) return false;
    const draft = value.draft;
    if (['create', 'edit'].includes(operation) && (!draft || !validText(draft.objective) || !draft.objective.trim())) return false;
    if (operation === 'budget' && (!draft || !positive(draft.tokenBudget))) return false;
    if (operation === 'create' && draft.tokenBudget && !positive(draft.tokenBudget)) return false;
    return true;
  };
  function acknowledgedDraft(operation, snapshot) {
    const draft = value.draft, goal = snapshot.goal;
    if (!draft || !goal || ['create', 'clear'].includes(operation)) return null;
    const next = { ...draft, goalID: goal.goal_id, revision: goal.revision };
    if (operation === 'edit') next.objective = goal.objective;
    if (operation === 'budget') next.tokenBudget = goal.token_budget ?? '';
    return next.objective !== goal.objective || next.tokenBudget !== (goal.token_budget ?? '') ? next : null;
  }
  return {
    state: () => value,
    canControl,
    canStage: () => ready() && value.snapshot.goal?.status !== 'complete' &&
      !context.activeTurnID && !foreign() && !stale(),
    captureContext: () => ({ epoch: contextEpoch, sessionID: context.sessionID }),
    matchesContext: ticket => ticket.epoch === contextEpoch && ticket.sessionID === context.sessionID,
    updateContext(next) {
      const changed = next.sessionID !== context.sessionID || next.live !== context.live ||
        next.backendReady !== context.backendReady || next.parentView !== context.parentView;
      context = { ...next };
      if (changed) {
        epoch++; contextEpoch++;
        publish({ snapshot: null, status: 'idle', notice: value.draft ? foreignNotice : '' });
      }
    },
    stage({ objective, tokenBudget }) {
      if (!ready() || context.activeTurnID || foreign() || stale()) return false;
      const cas = expected(), previous = value.draft;
      value = { ...value, draft: { sessionID: context.sessionID,
        goalID: previous?.goalID ?? cas.expected_goal_id,
        revision: previous?.revision ?? cas.expected_revision, objective, tokenBudget } };
      return true;
    },
    discard() {
      if (['loading', 'saving'].includes(value.status)) return false;
      publish({ draft: null, notice: '' }); return true;
    },
    async load(discardDraft = false) {
      if (!usable() || ['loading', 'saving'].includes(value.status)) return false;
      if (discardDraft) publish({ draft: null, notice: '' });
      const request = ++epoch, target = context.sessionID;
      publish({ status: 'loading', notice: '' });
      try {
        const snapshot = normalizeGoalState(await api('getGoal', { sessionID: target }), target);
        if (request === epoch && usable() && context.sessionID === target) {
          value = { ...value, snapshot };
          publish({ status: 'ready', notice: foreign() ? foreignNotice : stale() ? staleNotice :
            snapshot.available ? '' : (snapshot.diagnostic || 'Goal 当前不可用。') });
        }
      } catch {
        if (request === epoch && usable() && context.sessionID === target) {
          publish({ status: 'error', notice: '读取 Goal 失败；可重试，草稿已保留。' });
        }
      }
      return true;
    },
    async control(operation) {
      if (!canControl(operation)) return false;
      const draft = value.draft;
      const payload = { sessionID: context.sessionID, operation, ...expected() };
      if (['create', 'edit'].includes(operation)) payload.objective = draft.objective.trim();
      if (operation === 'budget' || (operation === 'create' && draft.tokenBudget)) payload.token_budget = draft.tokenBudget;
      const request = ++epoch, target = context.sessionID;
      publish({ status: 'saving', notice: '正在更新 Goal…' });
      try {
        const response = await api('controlGoal', payload);
        const snapshot = normalizeGoalState(response?.state, target);
        if (request !== epoch || !usable() || context.sessionID !== target) return false;
        const starts = ['create', 'resume', 'continue'].includes(operation);
        const started = response?.started === true && response?.turn?.accepted === true;
        const notice = starts && !started
          ? response.saved ? '已保存，尚未开始；请确认后显式继续。' : '目标仍有效，尚未开始；请重试显式继续。'
          : started ? '已接受执行请求。' : operation === 'clear' ? '已清除 Goal。' : '已保存。';
        publish({ snapshot, draft: acknowledgedDraft(operation, snapshot), status: 'ready', notice });
        // HTTP admission never fabricates an SSE cursor or runtime event.
        if (started && starts) { onStarted(response.turn); void this.load(); }
        return true;
      } catch (error) {
        if (request === epoch && usable() && context.sessionID === target) {
          publish({ status: error?.status === 409 ? 'conflict' : 'error',
            notice: error?.status === 409 ? staleNotice : '更新 Goal 失败；草稿已保留，请重新读取后重试。' });
        }
        return false;
      }
    },
  };
}

export function createGoalView({ root, api, context, confirm = async () => true, onStarted }) {
  const node = (tag, name, copy) => {
    const item = document.createElement(tag);
    item.className = name;
    if (copy !== undefined) item.textContent = copy;
    return item;
  };
  const controller = createGoalController({ api, onChange: value => paint(value), onStarted });
  const toggle = node('button', 'goal-toggle', 'Goal · 持续目标');
  toggle.type = 'button';
  toggle.setAttribute('aria-expanded', 'false');
  toggle.setAttribute('aria-controls', 'goal-body');
  const body = node('div', 'goal-body');
  body.id = 'goal-body';
  const status = node('p', 'goal-status');
  status.setAttribute('role', 'status');
  const objective = document.createElement('textarea');
  objective.rows = 2;
  objective.placeholder = '要完成什么？（最多 4000 个字符）';
  objective.setAttribute('aria-label', 'Goal 目标');
  const budget = document.createElement('input');
  budget.inputMode = 'numeric';
  budget.placeholder = '令牌预算（正整数）';
  budget.setAttribute('aria-label', 'Goal 令牌预算');
  const objectiveLabel = node('label', 'goal-field', '目标');
  const budgetLabel = node('label', 'goal-field', '令牌预算');
  objectiveLabel.append(objective);
  budgetLabel.append(budget);
  const actions = node('div', 'goal-actions');
  const buttons = new Map();
  root.replaceChildren(toggle, body);
  body.append(status, objectiveLabel, budgetLabel, actions);
  body.hidden = true;
  async function run(operation) {
    const ticket = controller.captureContext();
    if (operation === 'reload') {
      const dirty = Boolean(controller.state().draft);
      if (dirty && !await confirm('丢弃未保存的 Goal 草稿并重新读取？', 'reload')) return;
      if (controller.matchesContext(ticket)) await controller.load(dirty);
      return;
    }
    if (operation === 'clear') {
      if (!controller.canControl(operation) || !await confirm('清除 Goal 会丢弃当前目标，确定继续？', 'clear')) return;
      if (!controller.matchesContext(ticket)) return;
    }
    const current = context();
    if (operation === 'pause' && current.activeTurnID) {
      try {
        await api('cancelTurn', { sessionID: current.sessionID, turnID: current.activeTurnID,
          mode: 'immediate', reason: 'Goal paused by user' });
        // Stop is a request; runtime events and the subsequent read own status.
      } catch {
        if (controller.matchesContext(ticket)) status.textContent = '请求停止失败；请重试。';
      }
      return;
    }
    await controller.control(operation);
  }
  for (const [operation, copy] of Object.entries({
    create: '创建并开始', edit: '保存目标', budget: '保存预算', resume: '恢复并继续',
    continue: '继续', pause: '暂停', reload: '重新读取', clear: '清除 Goal',
  })) {
    const button = node('button', operation === 'clear' ? 'quiet danger' : 'quiet', copy);
    button.type = 'button';
    button.dataset.goalOperation = operation;
    button.onclick = () => void run(operation);
    buttons.set(operation, button);
    actions.append(button);
  }
  const labels = { active: '进行中', paused: '已暂停', blocked: '受阻',
    budget_limited: '预算耗尽', usage_limited: '用量不完整', complete: '已完成' };
  function paint(value, preserveInputs = false) {
    const current = context(), snapshot = value.snapshot, goal = snapshot?.goal;
    const available = current.live && current.backendReady && snapshot?.available !== false;
    root.hidden = !current.live || current.parentView === false;
    toggle.disabled = !current.live || !current.backendReady;
    status.textContent = !current.backendReady ? '后端离线。' : value.notice ||
      (!snapshot ? '请重新读取当前 Goal。' : !snapshot.available ? (snapshot.diagnostic || 'Goal 当前不可用。') :
        goal ? labels[goal.status] + ' · 已用 ' + goal.tokens_used + (goal.token_budget ? ' / ' + goal.token_budget : '') : '尚未创建 Goal。');
    objectiveLabel.hidden = budgetLabel.hidden = !available;
    if (!preserveInputs) {
      const nextObjective = value.draft?.objective ?? goal?.objective ?? '';
      const nextBudget = value.draft?.tokenBudget ?? goal?.token_budget ?? '';
      if (objective.value !== nextObjective) objective.value = nextObjective;
      if (budget.value !== nextBudget) budget.value = nextBudget;
    }
    objective.disabled = budget.disabled = !controller.canStage();
    const shown = !goal ? ['create', 'reload'] : goal.status === 'complete' ? ['reload', 'clear'] :
      goal.status === 'active' ? ['edit', 'budget', 'continue', 'pause', 'reload', 'clear'] :
        goal.status === 'usage_limited' ? ['edit', 'budget', 'reload', 'clear'] :
          ['edit', 'budget', 'resume', 'reload', 'clear'];
    for (const [operation, button] of buttons) {
      button.hidden = !shown.includes(operation) || (!available && operation !== 'reload');
      button.disabled = operation === 'reload'
        ? !current.live || !current.backendReady || ['loading', 'saving'].includes(value.status)
        : operation === 'pause' && current.activeTurnID
          ? !current.backendReady || ['loading', 'saving'].includes(value.status)
          : !controller.canControl(operation);
    }
    if (snapshot?.available && !goal) {
      status.textContent += ' 未填写预算会沿用主机默认（' + (snapshot.default_token_budget ?? '无限') + '）；无限预算可能持续执行并消耗额度。';
    }
  }
  objective.oninput = budget.oninput = () => {
    controller.stage({ objective: objective.value, tokenBudget: budget.value });
    paint(controller.state(), true);
  };
  toggle.onclick = () => {
    body.hidden = !body.hidden;
    toggle.setAttribute('aria-expanded', String(!body.hidden));
    if (!body.hidden) void controller.load();
  };
  return {
    updateContext() { controller.updateContext(context()); paint(controller.state()); },
    refresh: () => controller.load(),
    controller,
  };
}
