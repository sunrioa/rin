import { actionById, preferredAction } from "./catalog.js";

// The fair comparison persists the one field this authored rule tree needs.
// It is intentionally not a stateless straw man.
export async function runRuleTree(store, preference, applyAction) {
  if (store.hasPendingRinWork()) {
    throw new Error("pending Rin work must be reconciled before rule-tree play");
  }
  await store.rememberPreference(preference);
  const savedPreference = store.game.preference;
  const action = savedPreference
    ? preferredAction(savedPreference)
    : actionById("offer.water");
  await store.applyBaselineAction(action);
  await applyAction(action);
  return {
    mode: "baseline",
    action,
    recalled: Boolean(savedPreference),
    provider_cost_usd: 0,
  };
}
