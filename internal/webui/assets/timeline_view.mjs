function messagePresentation(message) {
  return {
    role: message.role,
    content: message.content,
    reasoningContent: message.reasoningContent,
    toolLabels: JSON.stringify((message.toolCalls || []).map((call) => (
      call.name || call.function?.name || 'Tool'
    ))),
  };
}

function samePresentation(left, right) {
  return left.role === right.role &&
    left.content === right.content &&
    left.reasoningContent === right.reasoningContent &&
    left.toolLabels === right.toolLabels;
}

function keyFor(identity, occurrences) {
  const group = JSON.stringify(identity);
  const occurrence = occurrences.get(group) || 0;
  occurrences.set(group, occurrence + 1);
  return JSON.stringify([group, occurrence]);
}

function streamKey(message, occurrences) {
  // Sequence is deliberately excluded because it changes with each delta in
  // that same message. This is an alias for first durable-ID promotion only.
  return keyFor([
    'stream', message.turnID || '', message.role, message.toolCallID || '',
  ], occurrences);
}

function messageKey(message, occurrences) {
  return keyFor(['id', message.id], occurrences);
}

function disclosureStates(node) {
  const occurrences = new Map();
  return Array.from(node.querySelectorAll('details'), (details) => {
    const summary = details.querySelector('summary')?.textContent || '';
    const group = JSON.stringify([details.className, summary]);
    const occurrence = occurrences.get(group) || 0;
    occurrences.set(group, occurrence + 1);
    return [JSON.stringify([group, occurrence]), details];
  });
}

function retainDisclosures(previous, next) {
  const states = new Map(disclosureStates(previous).map(([key, details]) => (
    [key, details.open]
  )));
  for (const [key, details] of disclosureStates(next)) {
    if (states.has(key)) details.open = states.get(key);
  }
}

/** Keep unchanged message nodes, including browser-owned disclosure state. */
export function createTimelineRenderer(container, renderMessage) {
  let activeSessionID;
  let rendered = new Map();

  function reset() {
    container.replaceChildren();
    rendered.clear();
    activeSessionID = undefined;
  }

  function render(sessionID, messages) {
    if (sessionID !== activeSessionID) {
      reset();
      activeSessionID = sessionID;
    }

    const occurrences = new Map();
    const streamOccurrences = new Map();
    const next = new Map();
    for (const message of messages) {
      const streamAlias = streamKey(message, streamOccurrences);
      const key = message.id
        ? messageKey(message, occurrences)
        : streamAlias;
      const presentation = messagePresentation(message);
      let previous = rendered.get(key);
      if (!previous && message.id) {
        const streamed = rendered.get(streamAlias);
        if (streamed?.streaming) previous = streamed;
      }
      let node = previous?.node;
      if (!previous || !samePresentation(previous.presentation, presentation)) {
        node = renderMessage(message);
        if (previous) retainDisclosures(previous.node, node);
      }
      next.set(key, { presentation, node, streaming: !message.id });
    }

    const nodes = new Set(Array.from(next.values(), (entry) => entry.node));
    for (const child of Array.from(container.childNodes)) {
      if (!nodes.has(child)) container.removeChild(child);
    }
    let cursor = container.firstChild;
    for (const { node } of next.values()) {
      if (node === cursor) cursor = cursor.nextSibling;
      else container.insertBefore(node, cursor);
    }
    rendered = next;
  }

  return { render, reset };
}
