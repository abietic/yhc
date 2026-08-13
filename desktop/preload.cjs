const { contextBridge, ipcRenderer } = require('electron');

function subscribe(channel, listener) {
  if (typeof listener !== 'function') {
    throw new TypeError(`${channel} listener must be a function`);
  }
  const handler = (_event, payload) => listener(Object.freeze({ ...payload }));
  ipcRenderer.on(channel, handler);
  return () => ipcRenderer.removeListener(channel, handler);
}

contextBridge.exposeInMainWorld('yhcDesktop', Object.freeze({
  getInfo: () => ipcRenderer.invoke('app:get-info'),
  getProviderStatus: () => ipcRenderer.invoke('app:provider-status'),
  configureProvider: (input) => ipcRenderer.invoke('app:configure-provider', input),
  selectWorkspace: () => ipcRenderer.invoke('app:select-workspace'),
  api: (operation, payload = {}) => ipcRenderer.invoke('app:api', operation, payload),
  startEvents: (sessionID, after) => (
    ipcRenderer.invoke('app:events-start', { sessionID, after })
  ),
  stopEvents: (sessionID) => ipcRenderer.invoke('app:events-stop', sessionID),
  openWeb: () => ipcRenderer.invoke('app:open-web'),
  onEventStream: (listener) => subscribe('app:event-stream', listener),
  onBackendExit: (listener) => subscribe('app:backend-exit', listener),
}));
