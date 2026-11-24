"""Structural tool evidence from explicit operator artifacts; no execution authority."""

from collections import Counter, defaultdict
import hashlib
import json
from pathlib import Path
import re

from scripts.terminal_bench.budgeted_stream import usage_counters


# Do not publish private MCP/custom names, arguments, output or error prose.
PUBLIC_KINDS = frozenset(("Agent", "Bash", "BashOutput", "Edit", "Read", "Write", "Glob", "Grep",
                         "Skill", "WebFetch", "WebSearch", "TodoWrite",
                         "read_image", "job_output", "job_list", "job_kill"))
DSH_KINDS = {"bash": "Bash", "edit": "Edit", "read": "Read", "write": "Write", "glob": "Glob",
             "grep": "Grep", "skill": "Skill", "web_fetch": "WebFetch", "web_search": "WebSearch",
             "todo_write": "TodoWrite", "subagent": "Agent", "subagent_fork": "Agent"}


def _name(name):
    if not isinstance(name, str) or not name or len(name) > 512:
        raise ValueError("invalid tool name metadata")
    return name


def _kind(name):
    if name in DSH_KINDS:
        return DSH_KINDS[name]  # Retain raw names separately for exact receipt matching.
    return name if name in PUBLIC_KINDS else "other"


def _tool_id(value):
    if not isinstance(value, str) or not value or len(value) > 512:
        raise ValueError("invalid tool call metadata")
    return value


def _lines(path, *, with_sha256=False):
    path = Path(path)
    if path.is_symlink() or path.resolve() != path:
        raise ValueError("tool evidence must not use symbolic links")
    if not path.exists():
        return (None, None) if with_sha256 else None
    if not path.is_file() or path.stat().st_size > 64 * 1024 * 1024:
        raise ValueError("invalid tool evidence file")
    with path.open("rb") as stream:
        raw = stream.read(64 * 1024 * 1024 + 1)
    if len(raw) > 64 * 1024 * 1024:
        raise ValueError("invalid tool evidence file")
    lines = raw.decode("utf-8").replace("\r\n", "\n").replace("\r", "\n").split("\n")
    if lines[-1] == "":
        lines.pop()
    return (lines, hashlib.sha256(raw).hexdigest()) if with_sha256 else lines


def _unique(pairs):
    out = {}
    for key, value in pairs:
        if key in out:
            raise ValueError("duplicate tool evidence key")
        out[key] = value
    return out


def _events(lines):
    data, ended = [], False
    for line in lines:
        if line == "":
            if data:
                value = "\n".join(data)
                if ended:
                    raise ValueError("event after stream terminal")
                if value == "[DONE]":
                    ended = True
                else:
                    item = json.loads(value, object_pairs_hook=_unique)
                    if not isinstance(item, dict):
                        raise ValueError("invalid stream event metadata")
                    yield item
                data = []
        elif line.startswith("data:"):
            data.append(line[5:].removeprefix(" "))
    if data:
        raise ValueError("unfinished stream event")


def _reasoning_output(items):
    return isinstance(items, list) and all(isinstance(item, dict) and item.get("type") == "reasoning"
                                          for item in items)


def _reasoning_stream_event(event, response_id):
    kind = event.get("type")
    if kind in ("response.created", "response.in_progress"):
        response = event.get("response")
        return (isinstance(response, dict) and response.get("id") == response_id
                and response.get("status") == "in_progress"
                and _reasoning_output(response.get("output")))
    if kind in ("response.output_item.added", "response.output_item.done"):
        return _reasoning_output([event.get("item")])
    if kind in ("response.content_part.added", "response.content_part.done"):
        part = event.get("part")
        return isinstance(part, dict) and part.get("type") == "reasoning_text"
    return kind in ("response.reasoning_text.delta", "response.reasoning_text.done")


def _reasoning_limit(response, response_id, record):
    if not isinstance(response, dict) or response.get("id") != response_id or response.get("status") != "incomplete":
        return False
    details, output = response.get("incomplete_details"), response.get("output")
    if not isinstance(details, dict) or details.get("reason") != "max_output_tokens":
        return False
    if not output or not _reasoning_output(output):
        return False
    counts = usage_counters("responses", response.get("usage"))
    return (counts["output_tokens"] > 0 and counts["output_tokens"] == counts["thinking_tokens"]
            and all(type(record.get(key)) is int and record[key] == counts[key]
                    for key in ("input_tokens", "cached_input_tokens", "output_tokens", "thinking_tokens")))


def _response_tools(lines, protocol):
    """Return the tool ID-to-name mapping used by archived trajectory readers."""
    tools, _ = _response_tool_inventory(lines, protocol)
    return tools


def _response_tool_inventory(lines, protocol, *, verified_record=None):
    tools, stopped, begun, started, finished = {}, 0, 0, set(), set()
    reasoning_only = protocol == "responses" and verified_record is not None and verified_record.get("usage_known") is True
    reasoning_limit, response_id = False, None
    for index, event in enumerate(_events(lines)):
        kind = event.get("type")
        if stopped and kind != "ping":
            raise ValueError("event after response terminal")
        if reasoning_only:
            if index == 0 and kind == "response.created" and isinstance(event.get("response"), dict):
                response_id = event["response"].get("id")
            sequence = event.get("sequence_number")
            reasoning_only = (isinstance(response_id, str) and bool(response_id)
                              and (index == 0 or kind != "response.created")
                              and type(sequence) is int and sequence == index)
        if kind == "response.incomplete" and reasoning_only and _reasoning_limit(
            event.get("response"), response_id, verified_record
        ):
            stopped += 1
            reasoning_limit = True
            continue
        if kind in ("error", "response.failed", "response.incomplete"):
            raise ValueError("unsuccessful response terminal")
        if reasoning_only:
            reasoning_only = _reasoning_stream_event(event, response_id)
        if protocol == "responses" and kind == "response.completed":
            response = event.get("response")
            output = response.get("output") if isinstance(response, dict) else None
            if not isinstance(output, list):
                raise ValueError("full response inventory unavailable")
            stopped += 1
            for item in output:
                if not isinstance(item, dict):
                    raise ValueError("invalid output item metadata")
                if item.get("type") == "function_call":
                    identifier = _tool_id(item.get("call_id"))
                    if identifier in tools:
                        raise ValueError("duplicate tool call in response")
                    tools[identifier] = _name(item.get("name"))
        elif protocol == "messages":
            if kind == "message_start":
                if begun or stopped or not isinstance(event.get("message"), dict):
                    raise ValueError("invalid message start")
                begun += 1
            elif kind in ("content_block_start", "content_block_stop"):
                if begun != 1 or stopped:
                    raise ValueError("block outside message")
                index = event.get("index")
                if type(index) is not int or not 0 <= index < 100000:
                    raise ValueError("invalid block index")
                if kind == "content_block_start":
                    if index in started:
                        raise ValueError("duplicate block start")
                    started.add(index)
                    block = event.get("content_block")
                    if not isinstance(block, dict):
                        raise ValueError("invalid block metadata")
                    if block.get("type") == "tool_use":
                        identifier = _tool_id(block.get("id"))
                        if identifier in tools:
                            raise ValueError("duplicate tool call in response")
                        tools[identifier] = _name(block.get("name"))
                else:
                    if index not in started or index in finished:
                        raise ValueError("unmatched block stop")
                    finished.add(index)
            elif kind == "message_delta":
                delta = event.get("delta")
                if isinstance(delta, dict) and delta.get("stop_reason") in (
                    "max_tokens", "model_context_window_exceeded",
                ):
                    raise ValueError("incomplete message output")
            elif kind == "message_stop":
                stopped += 1
                if begun != 1 or started != finished:
                    raise ValueError("unfinished message blocks")
    if stopped != 1 or started != finished:
        raise ValueError("complete response terminal unavailable")
    return tools, reasoning_limit


def _dsh_native(lines, session_id):
    """Read SDK notifications from one explicitly named session tree, never tool prose."""
    root = _tool_id(session_id)
    starts, terminals, children = defaultdict(list), defaultdict(list), defaultdict(set)
    owners = defaultdict(set)
    events, valid = [], True
    for line in lines:
        try:
            row = json.loads(line, object_pairs_hook=_unique)
            if not isinstance(row, dict):
                raise ValueError("invalid SDK notification")
            if row.get("method") != "session.event":
                continue
            payload = row.get("payload")
            if not isinstance(payload, dict) or not isinstance(payload.get("event"), dict):
                raise ValueError("invalid SDK event")
            session = _tool_id(payload.get("sessionId"))
            event = payload["event"]
            if not isinstance(event.get("type"), str) or not event["type"] or not isinstance(event.get("data"), dict):
                raise ValueError("invalid SDK event data")
            if event.get("type") == "subagent/catalog":
                children[session].add(_tool_id(event["data"].get("childId")))
            events.append((session, event))
        except (ValueError, TypeError):
            valid = False
    allowed, pending = {root}, [root]
    while pending:
        for child in children[pending.pop()]:
            if child not in allowed:
                allowed.add(child)
                pending.append(child)
    seen = False
    for session, event in events:
        if session not in allowed:
            valid = False
            continue
        seen = True
        data = event["data"]
        try:
            if event.get("type") == "tool/call":
                identifier = _tool_id(data.get("callId"))
                starts[identifier].append(_name(data.get("name")))
                owners[identifier].add(session)
            elif event.get("type") == "tool/result":
                message = data.get("message")
                if not isinstance(message, dict) or message.get("role") != "tool":
                    raise ValueError("invalid SDK tool result")
                identifier = _tool_id(message.get("toolCallId"))
                source = message.get("source")
                if (not isinstance(source, dict) or source.get("kind") != "tool"
                        or source.get("callId") != identifier or type(message.get("isError")) is not bool
                        or owners.get(identifier) != {session}):
                    raise ValueError("unbound SDK tool result")
                terminals[identifier].append("failed" if message["isError"] else "completed")
        except (ValueError, TypeError):
            valid = False
    return "available" if valid and seen else "partial", starts, terminals


def _native(path, *, native_format="yhc_events", native_session_id=None):
    if native_format not in ("yhc_events", "dsh_sdk"):
        raise ValueError("unsupported native evidence format")
    if native_format == "dsh_sdk":
        _tool_id(native_session_id)  # A different session must never supply a success receipt.
    starts, terminals = defaultdict(list), defaultdict(list)
    try:
        lines = _lines(path)
    except UnicodeError:
        return "partial", starts, terminals
    if lines is None:
        return "unavailable", starts, terminals
    if native_format == "dsh_sdk":
        return _dsh_native(lines, native_session_id)
    valid = True
    for line in lines:
        try:
            row = json.loads(line, object_pairs_hook=_unique)
            if not isinstance(row, dict):
                raise ValueError("invalid native event")
            if row.get("type") != "event":
                continue
            event = row.get("event")
            if not isinstance(event, dict):
                raise ValueError("invalid native event metadata")
            if event.get("kind") not in ("tool_start", "tool_terminal"):
                continue
            if type(row.get("schema_version")) is not int or row["schema_version"] != 1:
                raise ValueError("unsupported native event schema")
            tool = event.get("tool")
            if not isinstance(tool, dict):
                raise ValueError("invalid native tool metadata")
            identifier = _tool_id(tool.get("tool_call_id"))
            if event["kind"] == "tool_start":
                starts[identifier].append(_name(tool.get("tool_name")))
            else:
                outcome = tool.get("outcome")
                if outcome not in ("completed", "failed"):
                    raise ValueError("unsupported native outcome")
                terminals[identifier].append(outcome)
        except (ValueError, TypeError):
            valid = False
    return "available" if valid else "partial", starts, terminals


def collect_tool_feedback(wire, records, protocol, native_path, *, native_format="yhc_events", native_session_id=None):
    """Count emitted calls separately from native terminal receipts and side effects.

    Missing/partial streams are lower bounds. A verified reasoning-only
    truncation has an empty tool inventory even though generation stopped.
    Only a complete emitted Edit inventory matched to unique successful native terminals proves zero Edit
    rejections. A generic failed outcome cannot identify a rejection category.
    Current receipts bind response bytes. Older receipts without that field
    retain structural coverage, reported separately from verified responses.
    """
    tools, complete, reasoning_only_truncations = [], 0, 0
    verified, legacy_unpinned, rejected = 0, 0, 0
    supported = protocol in ("responses", "messages")
    for record in records if supported else []:
        identifier = record["id"]
        if not re.fullmatch(r"[A-Za-z0-9_.:-]{1,180}", identifier) or identifier in (".", ".."):
            raise ValueError("unsafe wire call identifier")
        if record["state"] != "known":
            continue
        path = Path(wire) / (identifier + ".response.sse")
        try:
            lines, actual_sha256 = _lines(path, with_sha256=True)
        except UnicodeError:
            continue
        if lines is None:
            continue
        pinned = "response_sha256" in record
        if pinned and record["response_sha256"] != actual_sha256:
            rejected += 1
            continue
        try:
            response, reasoning_only_limit = _response_tool_inventory(
                lines, protocol, verified_record=record if pinned else None)
        except ValueError:
            continue
        complete += 1
        reasoning_only_truncations += int(reasoning_only_limit)
        if pinned:
            verified += 1
        else:
            legacy_unpinned += 1
        tools.extend(response.items())
    inventory = ("complete" if supported and complete == len(records) else
                 "partial" if complete else "unavailable")
    native, starts, terminals = _native(native_path, native_format=native_format, native_session_id=native_session_id)
    ids = Counter(identifier for identifier, _ in tools)

    def matched(identifier, kind):
        return (ids[identifier] == 1 and starts.get(identifier) == [kind]
                and len(terminals.get(identifier, [])) == 1)

    edits = [(identifier, kind) for identifier, kind in tools if _kind(kind) == "Edit"]
    matched_edits = [identifier for identifier, kind in edits if matched(identifier, kind)]
    edit_complete = (inventory == "complete" and native == "available" and bool(edits)
                     and len(matched_edits) == len(edits))
    completed_edits = sum(terminals[identifier] == ["completed"] for identifier in matched_edits)
    failed_edits = len(matched_edits) - completed_edits
    available = inventory == "complete" or complete > 0
    return dict(wire_inventory=inventory, complete_responses=complete, expected_responses=len(records),
                verified_reasoning_only_truncations=reasoning_only_truncations,
                wire_response_integrity=dict(verified_responses=verified, legacy_unpinned_responses=legacy_unpinned,
                                             rejected_responses=rejected),
                emitted_counts=dict(Counter(_kind(kind) for _, kind in tools)) if available else None,
                native_inventory=native,
                native_start_counts=(dict(Counter(_kind(kind) for values in starts.values() for kind in values))
                                     if native != "unavailable" else None),
                native_terminal_counts=(dict(Counter(outcome for values in terminals.values() for outcome in values))
                                        if native != "unavailable" else None),
                unmatched_emitted_calls=(sum(not matched(identifier, kind) for identifier, kind in tools)
                                         if available and native != "unavailable" else None),
                edit=dict(emitted=len(edits) if available else None,
                          matched_terminals=len(matched_edits) if native != "unavailable" else None,
                          completed=completed_edits if native != "unavailable" else None,
                          failed=failed_edits if native != "unavailable" else None,
                          coverage="complete" if edit_complete else "not_applicable" if inventory == "complete" and not edits
                          else "partial" if edits else "unavailable"),
                edit_rejections=0 if edit_complete and failed_edits == 0 else None)
