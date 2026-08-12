function activeTurnSessions(response) {
  if (!response || !Array.isArray(response.sessions)) {
    throw new TypeError('bounded session summaries array required');
  }
  return response.sessions.filter(
    (session) => typeof session?.active_turn_id === 'string' && session.active_turn_id.length > 0,
  );
}

function activeTurnQuitPrompt(count) {
  const singular = count === 1;
  return {
    type: 'warning',
    buttons: ['Keep working', 'Quit and stop turns'],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
    message: `${count} active turn${singular ? '' : 's'} ${singular ? 'is' : 'are'} still running`,
    detail: 'Quitting now stops those turns. Durable transcripts remain resumable.',
  };
}

function quitInspectionFailurePrompt() {
  return {
    type: 'warning',
    buttons: ['Keep working', 'Quit and stop backend'],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
    message: 'Unable to verify active turns',
  };
}

module.exports = {
  activeTurnSessions,
  activeTurnQuitPrompt,
  quitInspectionFailurePrompt,
};
