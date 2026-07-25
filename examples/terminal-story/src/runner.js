import { RinClient, RinTransportError } from "@sunrioa/rin-sdk";
import { runRuleTree } from "./baseline.js";
import { runRinStory } from "./rin-adapter.js";

export async function runStory({
  baseUrl,
  token = "",
  mode = "auto",
  sessionId,
  preference,
  store,
  applyAction,
  client = new RinClient(baseUrl, { token, timeoutMs: 5000 }),
}) {
  if (mode === "baseline") {
    return runRuleTree(store, preference, applyAction);
  }
  if (mode !== "auto" && mode !== "rin") {
    throw new Error("mode must be auto, rin, or baseline");
  }

  if (mode === "auto") {
    try {
      await client.health();
    } catch (error) {
      // Fallback is safe only before a Session mutation. Once runRinStory
      // starts, uncertainty is surfaced for exact recovery instead.
      if (error instanceof RinTransportError) {
        const result = await runRuleTree(store, preference, applyAction);
        return { ...result, mode: "fallback", fallback_reason: error.code };
      }
      throw error;
    }
  }
  return runRinStory(client, store, {
    sessionId,
    preference,
    applyAction,
  });
}
