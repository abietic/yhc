const actions = ['allow', 'deny', 'ask'];
const scopes = ['user', 'project', 'local'];
const definitelyUnwritten = new Set(['settings_busy', 'settings_invalid', 'permission_settings_active_turn', 'permission_settings_unavailable', 'session_not_found']);
const validText = value => typeof value === 'string' && value.isWellFormed() && [...value].length <= 4096;
const validChange = change => change && ['add', 'remove'].includes(change.operation) && actions.includes(change.action) && validText(change.rule) && change.rule.trim() && !change.rule.includes('\0');
const messages = Object.freeze({
  settings_conflict: 'Configuration changed elsewhere. Your draft is preserved; discard it and reload before saving.',
  settings_busy: 'Another process is saving configuration. Your draft is preserved; retry after it finishes.',
  permission_settings_active_turn: 'Wait for execution and Plan approval to finish. Your draft is preserved.',
  permission_settings_unavailable: 'Permission configuration is unavailable for this runtime.',
  settings_invalid: 'Rules or configuration are invalid. Your draft is preserved; check the selected file outside this editor.',
  session_not_found: 'This session is no longer available. Open a live session and reload.',
  settings_timeout: 'The save timed out and may have completed. Your draft is preserved; reload to verify before retrying.',
});

export function normalizePermissionSettings(raw, sessionID, scope) {
  if (!raw || raw.session_id !== (sessionID || '') || raw.scope !== scope || !scopes.includes(scope) || typeof raw.revision !== 'string' || !/^[a-f0-9]{64}$/i.test(raw.revision) || !Array.isArray(raw.rules) || raw.rules.length > 256 || !Number.isSafeInteger(raw.hidden_rules) || raw.hidden_rules < 0 || typeof raw.runtime_applied !== 'boolean' || !['', 'runtime_reload_failed'].includes(raw.diagnostic)) throw new TypeError('Invalid permission settings');
  const rules = raw.rules.map(row => {
    if (!row || !['structured', 'legacy'].includes(row.format) || !validText(row.value) || !validText(row.tool_name) || !validText(row.input_pattern) || typeof row.editable !== 'boolean' || typeof row.truncated !== 'boolean' || (row.format === 'structured' ? !actions.includes(row.action) : row.action !== '' && !actions.includes(row.action))) throw new TypeError('Invalid permission rule');
    return {action: row.action, value: row.value, format: row.format, editable: row.editable && row.format === 'structured' && !row.truncated && Boolean(row.value.trim()) && !row.value.includes('\0'), truncated: row.truncated, tool_name: row.tool_name, input_pattern: row.input_pattern};
  });
  return {session_id: sessionID || '', scope, revision: raw.revision, rules, hidden_rules: raw.hidden_rules, runtime_applied: raw.runtime_applied, diagnostic: raw.diagnostic};
}

export function createPermissionSettingsController({api, onChange}) {
  let context = {}, scope = 'user', epoch = 0;
  let value = {snapshot: null, changes: [], status: 'idle', notice: '', retired: false};
  const publish = patch => {value = {...value, ...patch}; onChange(value);};
  const busy = () => ['loading', 'saving'].includes(value.status);
  const available = () => context.visible && context.backendReady && (scope === 'user' || context.sessionID);
  const payload = () => ({scope, ...(context.sessionID ? {sessionID:context.sessionID} : {})});
  return {
    state: () => value,
    scope: () => scope,
    updateContext(next) {
      const changed = next.sessionID !== context.sessionID || next.backendReady !== context.backendReady;
      const hidden = context.visible && !next.visible;
      context = {...next};
      if (changed) {
        const uncertain = value.status === 'saving'; epoch++;
        publish({snapshot:null, status:'idle', retired:Boolean(value.changes.length), notice:uncertain ? 'Context changed while saving. The earlier request may have completed; discard this draft and reload to verify.' : value.changes.length ? 'Context changed. The previous draft is preserved but cannot be saved here. Discard it and reload.' : ''});
      } else if (hidden && busy()) {
        epoch++; publish({status:'idle', retired:Boolean(value.changes.length), notice:value.status === 'saving' ? 'The earlier save may have completed. Discard this draft and reload to verify.' : ''});
      } else onChange(value);
    },
    setScope(next) {
      if (!scopes.includes(next) || value.changes.length || busy() || context.externalBusy) return false;
      scope = next; epoch++; publish({snapshot:null, status:'idle', notice:'', retired:false}); return true;
    },
    discard() {
      if (busy() || context.externalBusy) return false;
      epoch++; publish({changes:[], snapshot:value.retired ? null : value.snapshot, status:'idle', notice:'', retired:false}); return true;
    },
    stage(change) {
      if (!available() || busy() || context.externalBusy || value.retired || !value.snapshot || value.changes.length >= 32 || !validChange(change)) return false;
      if (value.changes.some(item => item.operation===change.operation && item.action===change.action && item.rule===change.rule)) return true;
      publish({changes:[...value.changes, {operation:change.operation, action:change.action, rule:change.operation==='add' ? change.rule.trim() : change.rule}], notice:'Unsaved rule changes'}); return true;
    },
    undo(index) {
      if (busy() || context.externalBusy || !Number.isInteger(index) || index<0 || index>=value.changes.length) return;
      publish({changes:value.changes.filter((_,i)=>i!==index), notice:'Unsaved rule changes'});
    },
    async load() {
      if (!available() || busy() || context.externalBusy || value.changes.length) return;
      const request = ++epoch, target = payload(); publish({status:'loading', notice:''});
      try {
        const raw = await api('getPermissionSettings', target);
        if (request !== epoch || !available()) return;
        publish({snapshot:normalizePermissionSettings(raw,target.sessionID,target.scope), status:'ready', retired:false, notice:'Selected-file rules · not an effective-policy or permission grant'});
      } catch (error) {
        if (request === epoch && available()) publish({snapshot:null, status:'error', notice:messages[error?.code] || 'Could not load permission configuration. Reload to retry.'});
      }
    },
    async save() {
      if (!available() || context.activeTurn || busy() || context.externalBusy || value.retired || value.status==='conflict' || !value.snapshot || !value.changes.length) return;
      const request = ++epoch, target = payload(), changes = value.changes.map(change=>({...change}));
      const revision = value.snapshot.revision; publish({status:'saving', notice:'Saving rule changes…'});
      try {
        const raw = await api('updatePermissionSettings',{...target, revision, changes});
        if (request !== epoch || !available()) return;
        const snapshot = normalizePermissionSettings(raw,target.sessionID,target.scope);
        publish({snapshot, changes:[], status:'ready', notice:snapshot.diagnostic==='runtime_reload_failed' ? 'Saved, but the selected session was not reloaded by this request. Verify configuration and active policy before continuing.' : snapshot.runtime_applied ? 'Saved and reloaded for this idle session. Other live sessions may observe the shared file through existing hot reload.' : 'Saved. New sessions load these rules; existing sessions may observe the shared file through hot reload.'});
      } catch (error) {
        if (request === epoch && available()) {
          const conflict=error?.code==='settings_conflict';
          publish({status:conflict ? 'conflict' : 'error', retired:conflict || !definitelyUnwritten.has(error?.code), notice:messages[error?.code] || 'The save could not be confirmed. Your draft is preserved; reload to verify before retrying.'});
        }
      }
    },
  };
}

export function createPermissionSettingsView({root, actionsRoot, api, context, confirmDiscard, onActivity}) {
  const node = (tag, className, text) => {const item=document.createElement(tag); if(className) item.className=className; if(text!==undefined) item.textContent=text; return item;};
  const button = (text, className='quiet') => {const item=node('button',className,text); item.type='button'; return item;};
  const heading=node('div','mcp-heading'); heading.append(node('h3','','Permission rules'));
  const selector=node('select'); selector.id='permission-rule-scope';
  for(const [value,label] of [['user','User rules'],['project','Project rules · shared'],['local','Local rules · private']]) {const option=node('option','',label); option.value=value; selector.append(option);}
  const label=node('label','setting-label','Configuration scope'); label.htmlFor=selector.id;
  const reload=button('Reload'), save=button('Save rule changes','primary'), discard=button('Discard draft');
  const controls=node('div','permission-rule-controls'); controls.append(label,selector,reload);
  const status=node('p','mcp-status'); status.setAttribute('role','status'); status.setAttribute('aria-live','polite');
  const list=node('div','permission-rule-list'), draft=node('div','permission-rule-draft');
  const editor=node('div','permission-rule-editor'); const action=node('select'); action.id='permission-rule-action';
  for(const value of ['ask','deny','allow']) {const option=node('option','',value); option.value=value; action.append(option);}
  const actionLabel=node('label','setting-label','Decision'); actionLabel.htmlFor=action.id;
  const input=node('input'); input.type='text'; input.id='permission-rule-input'; input.maxLength=8192; input.autocomplete='off'; input.spellcheck=false; input.placeholder='Bash(go test *) or Read';
  const inputLabel=node('label','setting-label','Rule'); inputLabel.htmlFor=input.id;
  const add=button('Add to draft'); const preview=node('p','setting-description'); preview.setAttribute('aria-live','polite');
  editor.append(actionLabel,action,inputLabel,input,add,preview);
  const footer=node('div','permission-rule-controls'); const summary=node('p','permission-footer-summary'); footer.append(summary,discard,save); actionsRoot.append(footer);
  root.append(heading,node('p','setting-description','Same files and parser as TUI · deny > ask > allow. This is selected-file configuration, not active policy. Other sessions may hot-reload shared files. Changes are saved only explicitly.'),controls,status,editor,draft,list);
  let visible=false, externalBusy=false;
  const controller=createPermissionSettingsController({api,onChange:paint});
  const busy=()=>['loading','saving'].includes(controller.state().status);
  function paint(value) {
    const current=context(), unavailable=!current.backendReady || (controller.scope()!=='user' && !current.sessionID);
    const blocked=busy() || externalBusy, frozen=blocked || unavailable || value.retired || !value.snapshot;
    selector.value=controller.scope(); selector.disabled=blocked;
    for(const option of selector.options) option.disabled=option.value!=='user' && !current.sessionID;
    reload.disabled=blocked || unavailable; save.disabled=frozen || Boolean(current.activeTurn) || value.status==='conflict' || !value.changes.length;
    discard.disabled=blocked || !value.changes.length; action.disabled=input.disabled=frozen;
    add.disabled=frozen || value.changes.length>=32 || !validChange({operation:'add',action:action.value,rule:input.value});
    status.textContent=!current.backendReady ? 'Backend offline. Reconnect before editing rules.' : unavailable ? 'Open a live session to edit workspace rules.' : value.notice || (busy() ? 'Loading rule configuration…' : 'Reload to inspect this scope.');
    summary.textContent=busy() ? (value.status==='saving' ? 'Saving rules…' : 'Loading rules…') : value.changes.length ? `${value.changes.length} unsaved rule changes` : 'No unsaved rule changes';
    if(current.activeTurn && value.snapshot) status.textContent+=' · Saving is disabled while execution is active.';
    list.replaceChildren(); draft.replaceChildren();
    if(value.snapshot) {
      if(!value.snapshot.rules.length) list.append(node('p','mcp-empty','No rules in this file. Other scopes and permission mode still apply.'));
      for(const row of value.snapshot.rules) {
        const item=node('section','permission-rule-row'); item.append(node('span','mcp-state',row.action || 'Unparsed legacy'),node('code','',row.value));
        item.append(node('p','setting-description',row.format==='legacy' ? 'Legacy format · preserved, read-only' : row.input_pattern==='' ? 'Tool-wide · all inputs' : 'Input-scoped pattern'));
        if(row.truncated) item.append(node('p','mcp-limit','Rule shortened · read-only'));
        const remove=button('Remove'); remove.disabled=!row.editable || frozen || value.changes.length>=32 || value.changes.some(change=>change.operation==='remove' && change.action===row.action && change.rule===row.value);
        remove.onclick=()=>controller.stage({operation:'remove',action:row.action,rule:row.value}); item.append(remove); list.append(item);
      }
      if(value.snapshot.hidden_rules) list.append(node('p','mcp-limit',`${value.snapshot.hidden_rules} rules omitted by display limits. They are preserved.`));
    }
    if(value.changes.length) {
      draft.append(node('h4','',`Draft · ${value.changes.length}/32 changes`));
      value.changes.forEach((change,index)=>{const item=node('div','permission-rule-row'); item.append(node('code','',`${change.operation} ${change.action} ${change.rule}`)); const undo=button('Undo'); undo.disabled=blocked; undo.onclick=()=>controller.undo(index); item.append(undo); draft.append(item);});
    }
    preview.textContent=input.value.trim() ? 'A bare tool name or Tool(*) applies to every input for that tool. Add stages a change; it does not execute or authorize a tool.' : 'Choose ask, deny or allow, then enter a TUI-compatible rule.';
    onActivity?.();
  }
  async function discardAllowed() {return !busy() && !externalBusy && (!controller.state().changes.length || await confirmDiscard());}
  function updateContext() {controller.updateContext({...context(),visible,externalBusy});}
  selector.onchange=async()=>{const next=selector.value; if(!await discardAllowed()) {selector.value=controller.scope(); return;} controller.discard(); controller.setScope(next); input.value=''; await controller.load();};
  reload.onclick=async()=>{if(await discardAllowed()) {controller.discard(); await controller.load();}};
  discard.onclick=async()=>{if(await discardAllowed()) {controller.discard(); input.value=''; paint(controller.state());}};
  add.onclick=()=>{if(controller.stage({operation:'add',action:action.value,rule:input.value})) {input.value=''; paint(controller.state()); input.focus();}};
  input.oninput=action.onchange=()=>paint(controller.state());
  input.onkeydown=event=>{if(event.key==='Enter') {event.preventDefault(); if(!add.disabled) add.click();}};
  save.onclick=()=>void controller.save();
  return {
    setVisible(next) {const entered=next && !visible; visible=next; root.hidden=actionsRoot.hidden=!next; updateContext(); if(entered && !controller.state().snapshot && !controller.state().changes.length) void controller.load();},
    updateContext, isBusy:busy, dirty:()=>Boolean(controller.state().changes.length),
    setBlocked(next) {if(externalBusy===next) return; externalBusy=next; updateContext(); if(!next && visible && controller.state().status==='idle' && !controller.state().snapshot && !controller.state().changes.length) void controller.load();},
    reset() {controller.discard(); input.value=''; controller.setScope('user');},
  };
}
