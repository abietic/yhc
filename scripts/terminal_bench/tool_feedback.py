"""Structural tool evidence from explicit operator artifacts; no execution authority."""

from collections import Counter, defaultdict
import json
from pathlib import Path
import re


# Do not publish private MCP/custom names, arguments, output or error prose.
PUBLIC_KINDS = frozenset(("Agent", "Bash", "BashOutput", "Edit", "Read", "Write", "Glob", "Grep",
                         "Skill", "WebFetch", "WebSearch", "TodoWrite"))


def _name(name):
    if not isinstance(name, str) or not name or len(name) > 512:
        raise ValueError("invalid tool name metadata")
    return name


def _kind(name):
    if name == "bash":
        return "Bash"  # Public DSH SDK tool name; retain raw names for receipt matching.
    return name if name in PUBLIC_KINDS else "other"


def _tool_id(value):
    if not isinstance(value, str) or not value or len(value) > 512:
        raise ValueError("invalid tool call metadata")
    return value


def _lines(path):
    path = Path(path)
    if path.is_symlink() or path.resolve() != path:
        raise ValueError("tool evidence must not use symbolic links")
    if not path.exists():
        return None
    if not path.is_file() or path.stat().st_size > 64 * 1024 * 1024:
        raise ValueError("invalid tool evidence file")
    lines = path.read_text(encoding="utf-8").split("\n")
    if lines[-1] == "":
        lines.pop()
    return lines


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


def _response_tools(lines, protocol):
    tools, stopped, begun, started, finished = {}, 0, 0, set(), set()
    for event in _events(lines):
        kind = event.get("type")
        if kind in ("error", "response.failed", "response.incomplete"):
            raise ValueError("unsuccessful response terminal")
        if stopped and kind != "ping":
            raise ValueError("event after response terminal")
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
            elif kind == "message_stop":
                stopped += 1
                if begun != 1 or started != finished:
                    raise ValueError("unfinished message blocks")
    if stopped != 1 or started != finished:
        raise ValueError("complete response terminal unavailable")
    return tools


def _native(path):
    starts, terminals = defaultdict(list), defaultdict(list)
    try:
        lines = _lines(path)
    except UnicodeError:
        return "partial", starts, terminals
    if lines is None:
        return "unavailable", starts, terminals
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


def collect_tool_feedback(wire, records, protocol, native_path):
    """Count emitted calls separately from native terminal receipts and side effects.

    Missing/partial streams are lower bounds. Only a complete emitted Edit
    inventory matched to unique successful native terminals proves zero Edit
    rejections. A generic failed outcome cannot identify a rejection category.
    """
    tools, complete = [], 0
    supported = protocol in ("responses", "messages")
    for record in records if supported else []:
        identifier = record["id"]
        if not re.fullmatch(r"[A-Za-z0-9_.:-]{1,180}", identifier) or identifier in (".", ".."):
            raise ValueError("unsafe wire call identifier")
        path = Path(wire) / (identifier + ".response.sse")
        try:
            lines = _lines(path)
        except UnicodeError:
            continue
        if lines is None or record["state"] != "known":
            continue
        try:
            response = _response_tools(lines, protocol)
        except ValueError:
            continue
        complete += 1
        tools.extend(response.items())
    inventory = ("complete" if supported and complete == len(records) else
                 "partial" if complete else "unavailable")
    native, starts, terminals = _native(native_path)
    ids = Counter(identifier for identifier, _ in tools)

    def matched(identifier, kind):
        return (ids[identifier] == 1 and starts.get(identifier) == [kind]
                and len(terminals.get(identifier, [])) == 1)

    edits = [(identifier, kind) for identifier, kind in tools if kind == "Edit"]
    matched_edits = [identifier for identifier, kind in edits if matched(identifier, kind)]
    edit_complete = (inventory == "complete" and native == "available" and bool(edits)
                     and len(matched_edits) == len(edits))
    completed_edits = sum(terminals[identifier] == ["completed"] for identifier in matched_edits)
    failed_edits = len(matched_edits) - completed_edits
    available = inventory == "complete" or complete > 0
    return dict(wire_inventory=inventory, complete_responses=complete, expected_responses=len(records),
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
