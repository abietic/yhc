import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import test from 'node:test';
import {operationPath} from '../../internal/webui/assets/transport.mjs';
import {createPermissionSettingsController, normalizePermissionSettings} from '../../internal/webui/assets/permission_settings.mjs';
const require = createRequire(import.meta.url);
const {desktopOperation} = require('../request.cjs');
const revision = 'a'.repeat(64);
const response = (sessionID = '', scope = 'user') => ({session_id: sessionID, scope, revision, rules: [], hidden_rules: 0, runtime_applied: false, diagnostic: ''});
const pending = () => {let resolve; const promise = new Promise(done => resolve = done); return {promise, resolve};};

test('both permission transports preserve only typed scope and atomic changes', () => {
  for (const operation of [operationPath, desktopOperation]) {
    assert.deepEqual(operation('getPermissionSettings', {}), ['/v1/permission-settings?scope=user', 'GET', undefined]);
    const changes = [{operation: 'add', action: 'deny', rule: 'Write'}];
    assert.deepEqual(operation('updatePermissionSettings', {scope:'local', sessionID:'owned', revision, changes}), ['/v1/permission-settings?scope=local&session_id=owned', 'PATCH', {revision, changes}]);
    for (const payload of [{scope:'local'}, {scope:'other'}, {scope:'user', path:'/private'}, {scope:'user', sessionID:'../escape'}]) assert.throws(() => operation('getPermissionSettings', payload));
    for (const bad of [{operation:'replace', action:'allow', rule:'Read'}, {operation:'add', action:'bypass', rule:'Read'}, {operation:'add', action:'allow', rule:'\0'}, {operation:'add', action:'allow', rule:'Read', secret:'fixture'}]) assert.throws(() => operation('updatePermissionSettings', {revision, changes:[bad]}));
  }
});

test('permission projection correlates ownership and keeps literal rule values', () => {
  const raw = response('owned', 'local');
  raw.rules = [{action:'deny', value:'<img src=x>', format:'structured', editable:true, truncated:false, tool_name:'<img src=x>', input_pattern:''}];
  const normalized = normalizePermissionSettings(raw, 'owned', 'local');
  assert.equal(normalized.rules[0].value, '<img src=x>');
  assert.throws(() => normalizePermissionSettings(raw, 'other', 'local'));
  assert.throws(() => normalizePermissionSettings({...raw, revision:'stale'}, 'owned', 'local'));
  assert.throws(() => normalizePermissionSettings({...raw, rules:Array(257).fill(raw.rules[0])}, 'owned', 'local'));
});

test('controller retires pending reads, preserves a draft on context changes, and never saves implicitly', async () => {
  const deferred = pending(); const calls = [];
  const controller = createPermissionSettingsController({api:(op,payload) => {calls.push([op,payload]); return calls.length===1 ? deferred.promise : Promise.resolve(response('owned','local'));}, onChange:()=>{}});
  controller.updateContext({sessionID:'old', backendReady:true, visible:true}); controller.setScope('local');
  const old = controller.load(); controller.updateContext({sessionID:'owned', backendReady:true, visible:true}); deferred.resolve(response('old','local')); await old;
  assert.equal(controller.state().snapshot, null);
  await controller.load(); controller.stage({operation:'add', action:'deny', rule:'Write'});
  controller.updateContext({sessionID:'next', backendReady:true, visible:true});
  assert.equal(controller.state().changes.length, 1); assert.equal(controller.state().retired, true);
  await controller.save(); assert.equal(calls.filter(([op])=>op==='updatePermissionSettings').length,0);
  controller.discard(); assert.equal(controller.state().changes.length,0);
});

test('conflicts preserve drafts and fixed messages; active turns cannot save', async () => {
  const calls = []; let conflict = false;
  const controller = createPermissionSettingsController({api:async(op,payload)=>{calls.push([op,payload]); if(conflict) throw {code:'settings_conflict', message:'fixture-private-path'}; return response('owned');}, onChange:()=>{}});
  controller.updateContext({sessionID:'owned', backendReady:true, visible:true}); await controller.load();
  controller.stage({operation:'add', action:'allow', rule:'Read'});
  controller.updateContext({sessionID:'owned', backendReady:true, visible:true, activeTurn:true}); await controller.save();
  assert.equal(calls.length,1);
  controller.updateContext({sessionID:'owned', backendReady:true, visible:true, activeTurn:false}); conflict=true; await controller.save();
  assert.equal(controller.state().changes.length,1); assert.equal(controller.state().status,'conflict'); assert.equal(controller.state().retired,true); assert.ok(!controller.state().notice.includes('fixture-private'));
  assert.equal(controller.stage({operation:'add',action:'deny',rule:'Write'}),false); await controller.save();
  assert.equal(calls.filter(([op])=>op==='updatePermissionSettings').length,1);
});

test('uncertain permission saves preserve and retire the exact draft until reload', async () => {
  for (const failure of [{code:'settings_timeout',message:'private timeout'}, new TypeError('private decode')]) {
    const calls=[];
    const controller=createPermissionSettingsController({api:async(op,payload)=>{calls.push([op,payload]); if(op==='updatePermissionSettings') throw failure; return response('owned');},onChange:()=>{}});
    controller.updateContext({sessionID:'owned',backendReady:true,visible:true}); await controller.load();
    const change={operation:'add',action:'deny',rule:'Write'}; controller.stage(change); await controller.save();
    assert.deepEqual(controller.state().changes,[change]); assert.equal(controller.state().retired,true);
    assert.equal(controller.state().notice.includes('private'),false);
    assert.equal(controller.stage({operation:'add',action:'allow',rule:'Read'}),false);
    await controller.save(); assert.equal(calls.filter(([op])=>op==='updatePermissionSettings').length,1);
    controller.discard(); await controller.load();
    assert.equal(calls.filter(([op])=>op==='getPermissionSettings').length,2);
    assert.equal(controller.state().retired,false); assert.deepEqual(controller.state().changes,[]);
  }
});

test('definitely unwritten permission failures remain correctable without reload', async () => {
  for (const code of ['settings_busy','settings_invalid','permission_settings_active_turn','permission_settings_unavailable','session_not_found']) {
    let writes=0;
    const controller=createPermissionSettingsController({api:async op=>{if(op==='updatePermissionSettings'){writes++; throw {code};} return response('owned');},onChange:()=>{}});
    controller.updateContext({sessionID:'owned',backendReady:true,visible:true}); await controller.load();
    controller.stage({operation:'add',action:'deny',rule:'Write'}); await controller.save();
    assert.equal(controller.state().retired,false,code); assert.equal(controller.state().changes.length,1,code);
    assert.equal(controller.stage({operation:'add',action:'allow',rule:'Read'}),true,code);
    await controller.save(); assert.equal(writes,2,code);
  }
});

test('committed reload failures clear saved drafts and remain an honest warning', async () => {
  const controller = createPermissionSettingsController({api:async op=>op==='getPermissionSettings' ? response('owned') : {...response('owned'), revision:'b'.repeat(64), diagnostic:'runtime_reload_failed'}, onChange:()=>{}});
  controller.updateContext({sessionID:'owned', backendReady:true, visible:true}); await controller.load(); controller.stage({operation:'add',action:'deny',rule:'Write'}); await controller.save();
  assert.equal(controller.state().changes.length,0); assert.match(controller.state().notice,/Saved.*not reloaded/);
});

test('shared-settings activity blocks permission operations without retiring its draft', async () => {
  const calls=[];
  const controller=createPermissionSettingsController({api:async(op)=>{calls.push(op); return response('owned');},onChange:()=>{}});
  const context={sessionID:'owned',backendReady:true,visible:true};
  controller.updateContext(context); await controller.load(); controller.stage({operation:'add',action:'deny',rule:'Write'});
  controller.updateContext({...context,externalBusy:true});
  assert.equal(controller.setScope('local'),false); assert.equal(controller.discard(),false);
  assert.equal(controller.stage({operation:'add',action:'allow',rule:'Read'}),false); controller.undo(0);
  await controller.load(); await controller.save();
  assert.deepEqual(calls,['getPermissionSettings']); assert.equal(controller.state().changes.length,1); assert.equal(controller.state().retired,false);
  controller.updateContext({...context,externalBusy:false}); await controller.save();
  assert.deepEqual(calls,['getPermissionSettings','updatePermissionSettings']); assert.equal(controller.state().changes.length,0);
});
