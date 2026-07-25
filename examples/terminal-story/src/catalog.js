export const PROTOCOL_VERSION = "rin.protocol/v1";

export const STORY_BINDING = Object.freeze({
  game_id: "rin-terminal-story",
  content_id: "last-station",
  content_version: "1.0.0",
  content_hash: "rin-terminal-story-last-station-v1",
});

export const ACTIONS = Object.freeze([
  Object.freeze({
    id: "offer.tea",
    kind: "preference.tea",
    description: "Mira remembers and places jasmine tea beside your ticket.",
  }),
  Object.freeze({
    id: "offer.coffee",
    kind: "preference.coffee",
    description: "Mira remembers and pours a small cup of coffee.",
  }),
  Object.freeze({
    id: "offer.water",
    kind: "neutral",
    description: "Mira sets down a glass of water, unsure what you prefer.",
  }),
]);

export function actionById(actionId) {
  const action = ACTIONS.find((candidate) => candidate.id === actionId);
  if (!action) throw new Error(`Rin selected an unknown action: ${actionId}`);
  return action;
}

export function preferredAction(preference) {
  return actionById(`offer.${preference}`);
}
