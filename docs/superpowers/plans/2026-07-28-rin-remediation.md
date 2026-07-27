# Rin Proposal Attempt CAS and Contract Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent stale Proposal responses from reviving a settled Attempt, require positive `MutationResult.revision` values in OpenAPI, and replace the root READMEs with shorter factual entry points.

**Architecture:** Keep the existing coordinator and Store boundaries. Change the Attempt update into a full-value compare-and-swap, strengthen Terminal Story settlement checks inside the existing file lock, and add one positive-integer schema used only by `MutationResult.revision`. Do not add dependencies or remove public SDK components.

**Tech Stack:** JavaScript ES modules with Node's built-in test runner, Go contract tests, OpenAPI 3.1 JSON, Markdown.

---

### Task 1: Reject stale Proposal Job persistence in the JavaScript SDK

**Files:**
- Modify: `sdk/javascript/test/client.test.js`
- Modify: `sdk/javascript/src/index.js`
- Modify: `sdk/javascript/src/index.d.ts`

- [ ] **Step 1: Add a failing coordinator test**

Add a test beside the existing Proposal Attempt test. The Store returns `false` from the CAS and the test proves that no Job poll occurs:

```js
test("Proposal Attempt stops when its Job persistence CAS loses", async () => {
  const original = {
    version: 1,
    operation_id: "operation.fixture",
    request: proposeRequest("request.fixture"),
    job_id: "",
  };
  let polls = 0;
  const store = {
    async loadProposalAttempt() { return structuredClone(original); },
    async createProposalAttempt() { return true; },
    async saveProposalAttempt(expected, replacement) {
      assert.deepEqual(expected, original);
      assert.equal(replacement.job_id, "job.fixture");
      return false;
    },
    async settleProposalAttempt() {},
  };
  const client = new RinClient(undefined, {
    fetch: async (url) => {
      if (new URL(url).pathname === "/v2/jobs/propose") {
        return response(202, {
          ok: true,
          data: { job_id: "job.fixture", status: "queued", duplicate: false },
        });
      }
      polls++;
      throw new Error("stale Attempt must not poll");
    },
  });

  await assert.rejects(
    new ProposalAttemptCoordinator(client, store).resume(),
    (error) => error instanceof RinConfigurationError &&
      error.code === "proposal_attempt_changed",
  );
  assert.equal(polls, 0);
});
```

- [ ] **Step 2: Run only the new SDK test and verify RED**

Run:

```powershell
node --test --test-name-pattern "Job persistence CAS loses" sdk/javascript/test/client.test.js
```

Expected: FAIL because `resume()` calls the old one-argument Store method and continues polling.

- [ ] **Step 3: Implement the minimal coordinator CAS**

In `ProposalAttemptCoordinator.resume()`, retain the Attempt read before submission and replace the unconditional save:

```js
const expected = attempt;
const replacement = { ...attempt, job_id: submission.job_id };
if (await this.store.saveProposalAttempt(expected, replacement) !== true) {
  throw new RinConfigurationError(
    "proposal_attempt_changed",
    "Proposal Attempt changed before its Job ID could be saved",
  );
}
attempt = replacement;
```

Do not add retry, polling, or compatibility fallback logic.

- [ ] **Step 4: Update the public TypeScript contract and existing test Stores**

Change the persistence signature to:

```ts
saveProposalAttempt(
  expected: ProposalAttempt,
  replacement: ProposalAttempt,
): Promise<boolean>;
```

Update existing in-memory test Stores to compare `expected`, assign `replacement`, and return a boolean. Remove the old one-argument test stubs.

- [ ] **Step 5: Run the JavaScript SDK package tests**

Run:

```powershell
npm test --prefix sdk/javascript
```

Expected: all SDK tests pass.

- [ ] **Step 6: Commit the SDK contract change**

```powershell
git add -- sdk/javascript/src/index.js sdk/javascript/src/index.d.ts sdk/javascript/test/client.test.js
git commit -m "fix: stop stale proposal attempt resumes"
```

### Task 2: Make Terminal Story Attempt updates and settlement compare full state

**Files:**
- Modify: `examples/terminal-story/test/story.test.js`
- Modify: `examples/terminal-story/src/workflow-store.js`

- [ ] **Step 1: Add a failing two-Store CAS regression**

Add a deterministic test using two Stores that share one file:

```js
test("a delayed Proposal response cannot revive a settled Attempt", async () => {
  const writer = await temporaryStore();
  await writer.ensureSessionId("session.fixture");
  await writer.beginRinTurn("tea", 1);
  const original = {
    version: 1,
    operation_id: "operation.fixture",
    request: { request_id: "request.fixture", session_id: "session.fixture" },
    job_id: "",
  };
  await writer.createProposalAttempt(original);
  const delayed = await new StoryWorkflowStore(writer.path).load();
  const active = { ...original, job_id: "job.fixture" };
  assert.equal(await writer.saveProposalAttempt(original, active), true);
  await writer.settleProposalAttempt({
    attempt: active,
    report: { request_id: "report.fixture" },
    apply: async () => writer.recordRinAction({ id: "offer.tea" }),
  });

  assert.equal(
    await delayed.saveProposalAttempt(original, { ...original, job_id: "job.delayed" }),
    false,
  );
  const persisted = await new StoryWorkflowStore(writer.path).load();
  assert.equal(persisted.document.attempt, null);
  assert.equal(persisted.game.pending_turn, null);
  assert.deepEqual(persisted.game.applied_action_ids, ["offer.tea"]);
  assert.equal(persisted.document.outbox.length, 1);
});
```

- [ ] **Step 2: Run only that Terminal Story test and verify RED**

Run:

```powershell
node --test --test-name-pattern "cannot revive a settled Attempt" examples/terminal-story/test/story.test.js
```

Expected: FAIL because `saveProposalAttempt` ignores `expected` and returns no CAS result.

- [ ] **Step 3: Replace the unconditional save with a full-value CAS**

Use the existing `isDeepStrictEqual` import and existing `commit` lock:

```js
async saveProposalAttempt(expected, replacement) {
  let saved = false;
  await this.commit((next) => {
    if (!isDeepStrictEqual(next.attempt, expected)) return NO_CHANGE;
    next.attempt = clone(replacement);
    saved = true;
  });
  return saved;
}
```

- [ ] **Step 4: Add failing settlement identity cases**

Use Terminal Story-shaped IDs in the fixture (`session.fixture.1.operation`,
`session.fixture.1.propose`, and `tick: 2`). Add subtests that mutate `job_id`,
`request.session_id`, and the durable pending turn before settlement. Each must
reject before `apply` runs.

```js
let applied = false;
await assert.rejects(
  store.settleProposalAttempt({
    attempt: staleAttempt,
    report: { request_id: "report.fixture" },
    apply: async () => { applied = true; },
  }),
  /changed before settlement|does not match/,
);
assert.equal(applied, false);
```

- [ ] **Step 5: Strengthen settlement under the file lock**

Replace the `operation_id`-only condition with `isDeepStrictEqual(next.attempt,
attempt)`. Before calling `apply`, require a Pending Turn and verify these
Terminal Story bindings:

```js
const turn = next.game.pending_turn;
const prefix = `${next.game.session_id}.${turn?.sequence}`;
if (!turn ||
    attempt.request.session_id !== next.game.session_id ||
    attempt.operation_id !== `${prefix}.operation` ||
    attempt.request.request_id !== `${prefix}.propose` ||
    attempt.request.tick !== turn.sequence * 2) {
  throw new Error("Proposal Attempt does not match the pending story turn");
}
```

Do not introduce a second lock or helper abstraction. Update existing
settlement fixtures to use the same Terminal Story identity format.

- [ ] **Step 6: Run the Terminal Story package tests**

Run:

```powershell
npm test --prefix examples/terminal-story
```

Expected: all Terminal Story tests pass.

- [ ] **Step 7: Commit the Store fix**

```powershell
git add -- examples/terminal-story/src/workflow-store.js examples/terminal-story/test/story.test.js
git commit -m "fix: compare proposal attempts before story writes"
```

### Task 3: Require positive MutationResult revisions in OpenAPI

**Files:**
- Modify: `api/contract_test.go`
- Modify: `api/openapi.json`
- Update only if generated by the existing tool: contract projections reported by `tools/generate_contract.py --check`

- [ ] **Step 1: Add the failing Contract assertion**

Extend `TestOpenAPIReferencesInputsAndResponseEvolutionRules`:

```go
positive, ok := schemas["JSONSafePositiveInteger"].(map[string]any)
if !ok {
    t.Fatalf("missing JSONSafePositiveInteger schema")
}
mutationRevision := schemas["MutationResult"].(map[string]any)["properties"].(map[string]any)["revision"].(map[string]any)
if positive["minimum"] != float64(1) ||
    positive["maximum"] != float64(protocol.MaxJSONSafeInteger) ||
    mutationRevision["$ref"] != "#/components/schemas/JSONSafePositiveInteger" {
    t.Fatalf("MutationResult revision must use the positive JSON-safe integer schema")
}
```

- [ ] **Step 2: Run the Contract test and verify RED**

Run:

```powershell
go test ./api -run TestOpenAPIReferencesInputsAndResponseEvolutionRules -count=1
```

Expected: FAIL because `JSONSafePositiveInteger` does not exist.

- [ ] **Step 3: Add the narrow OpenAPI schema change**

Add next to `JSONSafeUnsignedInteger`:

```json
"JSONSafePositiveInteger": {
  "type": "integer",
  "minimum": 1,
  "maximum": 9007199254740991
}
```

Change only `components.schemas.MutationResult.properties.revision.$ref` to `#/components/schemas/JSONSafePositiveInteger`.

- [ ] **Step 4: Run the Contract test and generator check**

Run:

```powershell
go test ./api -run TestOpenAPIReferencesInputsAndResponseEvolutionRules -count=1
python tools/generate_contract.py --check
```

Expected: both commands pass. If the generator reports tracked projections, regenerate them with the repository's documented generator command and inspect the diff before staging.

- [ ] **Step 5: Commit the Contract fix**

```powershell
git add -- api/openapi.json api/contract_test.go
git commit -m "fix: require positive mutation revisions"
```

### Task 4: Rewrite the root READMEs and remove duplicated entry-point prose

**Files:**
- Modify: `README.md`
- Modify: `README.en.md`
- Inspect, but do not delete without proof: all tracked `*.md`

- [ ] **Step 1: Preserve the factual source list**

Before rewriting, retain links to the documentation index, compatibility matrix, Protocol v2, release guide, operations guide, SDKs, examples, changelog, security policy, and license. Preserve the Preview status, supported Go version, loopback default, token/TLS boundary, and exact quick-start commands.

- [ ] **Step 2: Replace both root READMEs with concise entry points**

Use the same section order in both languages:

```text
Project status
What Rin does
Authority boundary
Quick start
Integration paths
Documentation
Repository layout
Security and deployment
License
```

Remove the long endpoint table and detailed persistence, Transfer, checkpoint, model-policy, and filesystem explanations from the root README. Their authoritative documents remain linked. Do not delete those documents.

- [ ] **Step 3: Apply the writing-anti-ai review**

Check both files for promotional adjectives, vague claims, repeated three-item lists, unnecessary bold text, repeated conclusions, and mechanical Chinese/English mirroring. Preserve code blocks and protocol names exactly.

- [ ] **Step 4: Re-run the redundant-document audit**

Run:

```powershell
$files = Get-ChildItem -Recurse -File -Filter '*.md'
$files | ForEach-Object {
  [PSCustomObject]@{
    Path = $_.FullName
    Hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash
  }
} | Group-Object Hash | Where-Object Count -gt 1
```

Expected: no exact duplicate documents. Do not delete semantic neighbors solely because they discuss the same subsystem. Remove a document only if it is byte-identical or explicitly superseded and every incoming link has a replacement.

- [ ] **Step 5: Check links and formatting without adding dependencies**

Run:

```powershell
git diff --check
rg -n "\]\([^)]*\.md\)" README.md README.en.md
```

Open each changed relative Markdown target with `Test-Path`. Expected: every target exists and `git diff --check` passes.

- [ ] **Step 6: Commit the README rewrite**

```powershell
git add -- README.md README.en.md
git commit -m "docs: shorten the project entry points"
```

### Task 5: Run only adjacent final verification

**Files:**
- Verify all files changed by Tasks 1-4

- [ ] **Step 1: Run the three relevant suites**

```powershell
npm test --prefix sdk/javascript
npm test --prefix examples/terminal-story
go test ./api -run 'TestContractMetadataAndRoutesMatchGeneratedRuntime|TestOpenAPIReferencesInputsAndResponseEvolutionRules' -count=1
python tools/generate_contract.py --check
```

Expected: all commands pass.

- [ ] **Step 2: Inspect the final diff for scope**

```powershell
git diff origin/main...HEAD --stat
git diff origin/main...HEAD --check
git status --short
```

Expected: only the design/plan, P1/P3 implementation and tests, plus the two root READMEs. No dependency files, generated caches, test output, or unrelated source changes.
