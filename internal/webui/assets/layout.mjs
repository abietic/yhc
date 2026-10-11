const WIDE_MIN = 1180;
const MEDIUM_MIN = 760;

export function responsiveMode(width) {
  const viewportWidth = Number.isFinite(Number(width)) ? Number(width) : 0;
  if (viewportWidth >= WIDE_MIN) return 'wide';
  if (viewportWidth >= MEDIUM_MIN) return 'medium';
  return 'compact';
}

export function normalizeOpenSheet(width, requestedKind) {
  const mode = responsiveMode(width);
  if (requestedKind === 'inspector') return requestedKind;
  if (mode === 'compact' && requestedKind === 'navigation') return requestedKind;
  return null;
}

export function sheetProjection(width, requestedKind) {
  const mode = responsiveMode(width);
  const openSheetKind = normalizeOpenSheet(width, requestedKind);
  const navigationExpanded = openSheetKind === 'navigation';
  const inspectorExpanded = openSheetKind === 'inspector';
  const backdropVisible = Boolean(openSheetKind) && mode !== 'wide';
  const navigationHidden = mode === 'compact' && !navigationExpanded;
  const inspectorHidden = !inspectorExpanded;

  return {
    mode,
    openSheetKind,
    navigationHidden,
    inspectorHidden,
    navigationExpanded,
    inspectorExpanded,
    backdropVisible,
    navigationInert: navigationHidden || (inspectorExpanded && backdropVisible),
    conversationInert: backdropVisible,
    inspectorInert: inspectorHidden || navigationExpanded,
  };
}

export function shouldCloseSheetOnEscape(key, dialogOpen, openSheetKind) {
  return key === 'Escape' &&
    !dialogOpen &&
    (openSheetKind === 'navigation' || openSheetKind === 'inspector');
}
