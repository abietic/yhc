"""Read-only proof of retired NEW dispatch capacity, never funding authority.

An external operator must pin the input ledger digest. This verifier does not
revalue historical holds, verify native sessions, allocate the arithmetic unused
cap, or authorize another sample. Old cohort formats are deliberately rejected.
"""

import uuid
from decimal import Decimal, localcontext

from scripts.terminal_bench.fresh_budget import BudgetStop, assess_plan, require, receipt_amount
from scripts.terminal_bench.reservation_bound import digest, request_bound, token_cost

SCOPE = 'benchmark-fresh-dispatch-retirement-v1'
TERMINAL = frozenset(('completed','failed','budget_paused'))


def _uuid(value):
    try: return isinstance(value,str) and str(uuid.UUID(value)) == value and uuid.UUID(value).version == 4
    except (ValueError,TypeError,AttributeError): return False


def _proof(plan, anchor, state, retirement_id):
    require(_uuid(retirement_id), 'retirement_identity_invalid')
    require(assess_plan(plan,anchor)['status'] == 'ready_offline', 'retirement_plan_invalid')
    require(isinstance(state,dict) and set(state) in (
                {'version','plan_sha256','currency','samples','calls','unknown_usage'},
                {'version','plan_sha256','currency','samples','calls','unknown_usage','retirement'})
            and type(state['version']) is int and state['version'] == 1 and state['plan_sha256'] == anchor
            and state['currency'] == plan['currency'] and state['unknown_usage'] is False, 'retirement_ledger_unverified')
    specs = {s['sample_id']:s for s in plan['samples']}
    require(isinstance(state['samples'],dict) and set(state['samples']) == set(specs), 'retirement_sample_coverage')
    started, unstarted = [], []
    for sample, row in state['samples'].items():
        require(isinstance(row,dict) and set(row) == {'status'}, 'retirement_sample_invalid')
        if row['status'] in TERMINAL: started.append(sample)
        else:
            require(row['status'] == ('retired' if 'retirement' in state else 'pending'), 'retirement_sample_still_live')
            unstarted.append(sample)
    calls, totals, counts = set(), {sample:Decimal(0) for sample in specs}, {sample:0 for sample in specs}
    require(isinstance(state['calls'],list) and len(state['calls']) <= len(specs)*plan['max_provider_calls'], 'retirement_calls_invalid')
    for row in state['calls']:
        require(isinstance(row,dict) and set(row) == {'id','sample','state','bound','liability','known_cost',
                'input_tokens','cached_input_tokens','output_tokens'} and _uuid(row['id']) and row['id'] not in calls
                and row['sample'] in started and row['state'] == 'known', 'retirement_call_unverified')
        calls.add(row['id']);spec = specs[row['sample']];bound = row['bound']
        require(isinstance(bound,dict), 'retirement_bound_invalid')
        requested = bound.get('requested_output_tokens')
        key = 'max_output_tokens' if spec['protocol'] == 'responses' else 'max_tokens'
        expected = request_bound(dict(model='deepseek-flash',**{key:requested}), spec['protocol'],
                                 plan['price_card'],plan['max_output_tokens_per_call'])
        # The frozen request's hash is anchored by the external ledger; all
        # pricing, protocol and reservation fields must be independently equal.
        body_hash = bound.get('body_sha256')
        require(isinstance(body_hash,str) and len(body_hash) == 64 and all(c in '0123456789abcdef' for c in body_hash),
                'retirement_body_binding_invalid')
        expected['body_sha256'] = body_hash
        require(bound == expected and type(bound.get('version')) is int, 'retirement_bound_invalid')
        counters = tuple(row[k] for k in ('input_tokens','cached_input_tokens','output_tokens'))
        require(all(type(n) is int and 0 <= n <= 2**64-1 for n in counters) and counters[1] <= counters[0]
                and counters[0] <= bound['input_tokens'] and counters[2] <= bound['output_tokens'], 'retirement_usage_invalid')
        cost = receipt_amount(token_cost(plan['price_card'],*counters))
        require(cost == receipt_amount(row['known_cost']) and receipt_amount(row['liability']) == receipt_amount(bound['reserved_amount'])
                and cost <= receipt_amount(row['liability']), 'retirement_cost_mismatch')
        totals[row['sample']] += cost;counts[row['sample']] += 1
    require(all(counts[s] <= plan['max_provider_calls'] and totals[s] <= receipt_amount(specs[s]['max_estimated_cost']) for s in specs),
            'retirement_sample_over_cap')
    known = sum(totals.values(),Decimal(0));cap = receipt_amount(plan['max_estimated_cost'])
    require(known <= cap, 'retirement_shared_over_cap')
    return dict(version=1,scope=SCOPE,retirement_id=retirement_id,plan_sha256=anchor,
                currency=plan['currency'],started_samples=sorted(started),unstarted_samples=sorted(unstarted),
                call_ids=sorted(calls),calls_sha256=digest(state['calls']),known_cost=format(known,'.12f'),
                shared_cap=format(cap,'.12f'),unused_cap=format(cap-known,'.12f'),dispatch_retired=True,
                native_joint_closure_verified=False,funding_release_authorized=False,can_resume_agent=False,
                automatic_paid_resume=False)


def retirement_record(plan,anchor,state):
    with localcontext() as context:
        context.prec = 90
        return _proof(plan,anchor,state,str(uuid.uuid4()))


def verify_retirement(plan,anchor,state,ledger_anchor):
    try:
        with localcontext() as context:
            context.prec = 90
            require(isinstance(ledger_anchor,str) and digest(state) == ledger_anchor, 'retirement_ledger_binding_invalid')
            require(isinstance(state,dict) and isinstance(state.get('retirement'),dict), 'retirement_not_committed')
            recorded = state['retirement']
            proof = _proof(plan,anchor,state,recorded.get('retirement_id'))
            require(recorded == proof, 'retirement_partition_or_totals_mismatch')
            return dict(proof,ledger_sha256=ledger_anchor)
    except BudgetStop: raise
    except (ValueError,KeyError,TypeError,AttributeError,ArithmeticError): raise BudgetStop('retirement_proof_invalid') from None


def project_funding(manifest,anchor):
    """Read-only conservation across an exhaustive operator-pinned inventory.

    Active/incompletely retired allocations retain their FULL caps. Historical
    holds remain fully committed. No registry write or new allocation is made;
    a caller still needs current source, native-closure and spending authority.
    """
    from scripts.terminal_bench.no_dispatch import identity, sha
    try:
        with localcontext() as context:
            context.prec = 90
            require(isinstance(manifest,dict) and digest(manifest) == anchor and sha(anchor)
                    and set(manifest) == {'version','scope','source_id','currency','source_cap','source_known_cost',
                                         'allocations','historical_reservations'}
                    and type(manifest['version']) is int and manifest['version'] == 1
                    and manifest['scope'] == 'benchmark-retired-capacity-projection-v1'
                    and identity(manifest['source_id']) and manifest['currency'] == 'CNY', 'retirement_funding_binding_invalid')
            cap,source_known = receipt_amount(manifest['source_cap']),receipt_amount(manifest['source_known_cost'])
            allocations,holds=manifest['allocations'],manifest['historical_reservations']
            require(cap > 0 and isinstance(allocations,list) and len(allocations) <= 128
                    and isinstance(holds,list) and len(holds) <= 128, 'retirement_funding_inventory_invalid')
            ids,cohorts,plans,retirements,calls,hold_ledgers=set(),set(),set(),set(),set(),set()
            active=known=unused=held=assigned=Decimal(0)
            for row in allocations:
                require(isinstance(row,dict) and set(row) == {'allocation_id','cap','retirement'}
                        and identity(row['allocation_id']) and row['allocation_id'] not in ids, 'retirement_allocation_reused_or_invalid')
                ids.add(row['allocation_id']);allocated=receipt_amount(row['cap']);assigned+=allocated
                require(allocated > 0, 'retirement_allocation_cap_invalid')
                proof=row['retirement']
                if proof is None:
                    active+=allocated
                    continue
                require(isinstance(proof,dict) and set(proof) == {'plan','plan_sha256','ledger','ledger_sha256'},
                        'retirement_allocation_proof_invalid')
                verified=verify_retirement(proof['plan'],proof['plan_sha256'],proof['ledger'],proof['ledger_sha256'])
                cohort=proof['plan']['cohort_id']
                require(proof['plan']['currency'] == manifest['currency'] and receipt_amount(verified['shared_cap']) == allocated,
                        'retirement_allocation_cap_mismatch')
                require(cohort not in cohorts and proof['plan_sha256'] not in plans and verified['retirement_id'] not in retirements,
                        'retirement_source_reused')
                require(not set(verified['call_ids']) & calls, 'retirement_call_reused')
                cohorts.add(cohort);plans.add(proof['plan_sha256']);retirements.add(verified['retirement_id']);calls.update(verified['call_ids'])
                known+=receipt_amount(verified['known_cost']);unused+=receipt_amount(verified['unused_cap'])
            for row in holds:
                require(isinstance(row,dict) and set(row) == {'reservation_id','amount','ledger_sha256'}
                        and identity(row['reservation_id']) and row['reservation_id'] not in ids
                        and sha(row['ledger_sha256']) and row['ledger_sha256'] not in hold_ledgers, 'retirement_historical_hold_reused_or_invalid')
                ids.add(row['reservation_id']);hold_ledgers.add(row['ledger_sha256'])
                liability=receipt_amount(row['amount']);require(liability > 0, 'retirement_historical_hold_invalid');held+=liability
            require(source_known+assigned+held <= cap, 'retirement_source_overallocated')
            committed=source_known+active+known+held
            return dict(version=1,scope=manifest['scope'],source_id=manifest['source_id'],currency=manifest['currency'],
                        manifest_sha256=anchor,source_cap=format(cap,'.12f'),source_known_cost=format(source_known,'.12f'),
                        retired_known_cost=format(known,'.12f'),retired_unused_cap=format(unused,'.12f'),
                        active_committed_caps=format(active,'.12f'),historical_retained=format(held,'.12f'),
                        total_committed=format(committed,'.12f'),unallocated_arithmetic=format(cap-committed,'.12f'),
                        funds_released='0.000000000000',funding_release_authorized=False,can_resume_agent=False,
                        automatic_paid_resume=False)
    except BudgetStop: raise
    except (ValueError,KeyError,TypeError,AttributeError,ArithmeticError): raise BudgetStop('retirement_funding_proof_invalid') from None
