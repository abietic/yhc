// Keep the startup wait alive and bounded. An unresolved promise alone does not
// prevent Node from exiting successfully when a fixture never acquires a handle.
function listenFixtureServer(server, timeoutMs = 5000) {
  return new Promise((resolve, reject) => {
    let settled = false;
    const timer = setTimeout(() => finish(new Error('Fixture server did not start before the startup deadline.')), timeoutMs);
    const onError = error => finish(error);
    function finish(error) {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      server.removeListener('error', onError);
      if (error) reject(error);
      else resolve();
    }
    server.once('error', onError);
    try {
      server.listen(0, '127.0.0.1', () => finish());
    } catch (error) {
      finish(error);
    }
  });
}

module.exports = { listenFixtureServer };
