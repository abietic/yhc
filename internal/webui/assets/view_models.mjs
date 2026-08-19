const variantKeys = Object.freeze([
  'permission',
  'question',
  'plan_approval',
  'repeated_tool',
]);

const permissionScopes = Object.freeze([
  'allow_once',
  'allow_session',
  'allow_always',
]);

const permissionEvidence = new Set([
  'Access\0Reads data',
  'Access\0May make destructive changes',
  'Access\0May change data',
  'Access\0May perform an action',
  'Path scope\0Within workspace',
  'Path scope\0Outside workspace boundary',
  'Network\0Uses network access',
  'Process\0Starts a child process',
]);

const planTargetModes = new Set([
  'default',
  'acceptEdits',
  'bypassPermissions',
  'dontAsk',
  'auto',
  'bubble',
]);

const repeatedToolExplanation = 'This repeated tool call needs your decision.';

function record(value) {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function string(value) {
  return typeof value === 'string' ? value : '';
}

function runeLength(value) {
  return [...value].length;
}

function boundedString(value, maximum, required = false) {
  return typeof value === 'string' &&
    runeLength(value) <= maximum &&
    (!required || value.trim().length > 0);
}

function exactVariant(interaction, expected) {
  return record(interaction?.[expected]) &&
    variantKeys.filter((key) => interaction?.[key] !== undefined).length === 1;
}

function base(interaction, kind, actionable) {
  return {
    requestID: string(interaction.request_id),
    turnID: string(interaction.turn_id),
    kind,
    actionable,
  };
}

function validEnvelope(interaction, expected) {
  return boundedString(interaction?.request_id, 512, true) &&
    boundedString(interaction?.turn_id, 512, true) &&
    exactVariant(interaction, expected);
}

function normalizePermission(value) {
  const evidence = Array.isArray(value?.evidence)
    ? value.evidence.map((item) => ({
      label: string(item?.label),
      value: string(item?.value),
    }))
    : [];
  const grantScopes = Array.isArray(value?.grant_scopes)
    ? value.grant_scopes.filter((item) => typeof item === 'string')
    : [];
  return {
    available: value?.available === true,
    toolLabel: string(value?.tool_label),
    summary: string(value?.summary),
    evidence,
    grantScopes,
  };
}

function validPermission(value, normalized) {
  if (!record(value) || typeof value.available !== 'boolean' ||
      !Array.isArray(value.evidence) || !Array.isArray(value.grant_scopes) ||
      normalized.evidence.length > 6 ||
      normalized.evidence.some((item) => (
        !boundedString(item.label, 48, true) || !boundedString(item.value, 160, true) ||
        !permissionEvidence.has(`${item.label}\0${item.value}`)
      ))) {
    return false;
  }
  const evidenceLabels = normalized.evidence.map((item) => item.label);
  if (new Set(evidenceLabels).size !== evidenceLabels.length) return false;
  const scopes = normalized.grantScopes;
  if (scopes.length !== value.grant_scopes.length ||
      new Set(scopes).size !== scopes.length ||
      scopes.some((scope) => !permissionScopes.includes(scope))) {
    return false;
  }
  if (!normalized.available) {
    return normalized.toolLabel === '' && normalized.summary === '' &&
      normalized.evidence.length === 0 &&
      scopes.length === 1 && scopes[0] === 'allow_once';
  }
  const validGrantScopes =
    (scopes.length === 1 && scopes[0] === 'allow_once') ||
    (scopes.length === permissionScopes.length &&
      permissionScopes.every((scope, index) => scopes[index] === scope));
  return boundedString(normalized.toolLabel, 96, true) &&
    normalized.summary === 'Allow this tool action?' &&
    evidenceLabels.filter((label) => label === 'Access').length === 1 &&
    validGrantScopes;
}

function normalizeQuestions(value) {
  return Array.isArray(value?.questions)
    ? value.questions.map((question) => ({
      id: string(question?.id),
      header: string(question?.header),
      text: string(question?.text),
      multiSelect: question?.multi_select === true,
      freeText: question?.free_text === true,
      options: Array.isArray(question?.options)
        ? question.options.map((option) => ({
          id: string(option?.id),
          label: string(option?.label),
          description: string(option?.description),
        }))
        : [],
    }))
    : [];
}

function normalizedIdentity(value) {
  return value.trim().toLowerCase().replaceAll(/\s+/g, ' ');
}

function validQuestions(value, questions) {
  if (!record(value) || !Array.isArray(value.questions) ||
      questions.length < 1 || questions.length > 4 ||
      questions.length !== value.questions.length) {
    return false;
  }
  const questionIdentities = new Set();
  for (let index = 0; index < questions.length; index += 1) {
    const raw = value.questions[index];
    const question = questions[index];
    const options = question.options;
    if (!record(raw) || raw.id !== `q-${index + 1}` ||
        typeof raw.multi_select !== 'boolean' || typeof raw.free_text !== 'boolean' ||
        !Array.isArray(raw.options) || raw.options.length !== options.length ||
        !boundedString(question.header, 12) || !boundedString(question.text, 1024, true) ||
        (options.length !== 0 && (options.length < 2 || options.length > 4)) ||
        question.freeText !== (options.length === 0)) {
      return false;
    }
    const questionIdentity = normalizedIdentity(question.text);
    if (questionIdentities.has(questionIdentity)) return false;
    questionIdentities.add(questionIdentity);
    const optionIdentities = new Set();
    for (let optionIndex = 0; optionIndex < options.length; optionIndex += 1) {
      const rawOption = raw.options[optionIndex];
      const option = options[optionIndex];
      if (!record(rawOption) || rawOption.id !== `q-${index + 1}-o-${optionIndex + 1}` ||
          !boundedString(option.label, 128, true) ||
          !boundedString(option.description, 1024)) {
        return false;
      }
      const optionIdentity = normalizedIdentity(option.label);
      if (optionIdentities.has(optionIdentity)) return false;
      optionIdentities.add(optionIdentity);
    }
  }
  // The server bounds the authored question JSON at 16 KiB before adding
  // request-scoped presentation IDs. Keep the projected DTO bounded without
  // rejecting a valid near-limit server projection because of that overhead.
  return new TextEncoder().encode(JSON.stringify(value.questions)).length <= 32 * 1024;
}

function normalizePlanApproval(value) {
  return {
    revision: Number(value?.revision || 0),
    targetModes: Array.isArray(value?.target_modes)
      ? value.target_modes.filter((item) => typeof item === 'string')
      : [],
    reviewAvailable: value?.review_available === true,
  };
}

function validPlanApproval(value, normalized) {
  const modes = normalized.targetModes;
  return record(value) && Number.isSafeInteger(value.revision) && value.revision > 0 &&
    typeof value.review_available === 'boolean' && Array.isArray(value.target_modes) &&
    modes.length === value.target_modes.length && modes.length >= 2 && modes.length <= 3 &&
    new Set(modes).size === modes.length &&
    modes.every((mode) => planTargetModes.has(mode)) &&
    modes.includes('acceptEdits') && modes.includes('bypassPermissions');
}

function normalizeRepeatedTool(value) {
  return {
    attempt: Number(value?.attempt || 0),
    explanation: string(value?.explanation),
    outcomes: Array.isArray(value?.outcomes)
      ? value.outcomes.filter((item) => item === 'continue' || item === 'stop')
      : [],
  };
}

function validRepeatedTool(value, normalized) {
  return record(value) && Number.isSafeInteger(value.attempt) && value.attempt > 0 &&
    value.explanation === repeatedToolExplanation && Array.isArray(value.outcomes) &&
    value.outcomes.length === 2 && normalized.outcomes.length === 2 &&
    new Set(normalized.outcomes).size === 2;
}

export function interactionViewModel(interaction = {}) {
  switch (interaction.kind) {
    case 'permission': {
      const permission = normalizePermission(interaction.permission);
      return {
        ...base(
          interaction,
          'permission',
          validEnvelope(interaction, 'permission') &&
            validPermission(interaction.permission, permission),
        ),
        permission,
      };
    }
    case 'question': {
      const questions = normalizeQuestions(interaction.question);
      return {
        ...base(
          interaction,
          'question',
          validEnvelope(interaction, 'question') &&
            validQuestions(interaction.question, questions),
        ),
        question: { questions },
      };
    }
    case 'plan_approval': {
      const planApproval = normalizePlanApproval(interaction.plan_approval);
      return {
        ...base(
          interaction,
          'plan_approval',
          validEnvelope(interaction, 'plan_approval') &&
            validPlanApproval(interaction.plan_approval, planApproval),
        ),
        planApproval,
      };
    }
    case 'repeated_tool': {
      const repeatedTool = normalizeRepeatedTool(interaction.repeated_tool);
      return {
        ...base(
          interaction,
          'repeated_tool',
          validEnvelope(interaction, 'repeated_tool') &&
            validRepeatedTool(interaction.repeated_tool, repeatedTool),
        ),
        repeatedTool,
      };
    }
    default:
      return base(interaction, 'unknown', false);
  }
}
