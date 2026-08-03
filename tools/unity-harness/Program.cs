using System;
using System.IO;
using System.Reflection;
using System.Text.Json;
using UnityEngine;
using UnityEngine.SceneManagement;

internal static class Program
{
    private static int Main()
    {
        var root = Path.Combine(Path.GetTempPath(), "rin-unity-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            Application.persistentDataPath = root;
            var first = new RinUnityWorkflow();
            Invoke(first, "Awake");
            Require((bool)Get(first, "authoritativeStateReady"), "fresh state did not open");
            var runId = (string)Get(first, "runId");
            var path = (string)Get(first, "statePath");
            var firstHost = (long)Get(first, "hostEpoch");
            var firstTimeline = (long)Get(first, "timelineEpoch");
            Require(File.Exists(path), "initialized state was not persisted");
            Invoke(first, "OnDestroy");

            var restarted = new RinUnityWorkflow();
            Invoke(restarted, "Awake");
            Require((string)Get(restarted, "runId") == runId, "restart minted a new identity");
            Require((long)Get(restarted, "hostEpoch") > firstHost,
                "domain reload did not advance Host generation");
            Require((long)Get(restarted, "timelineEpoch") > firstTimeline,
                "domain reload did not fork Timeline generation");

            var oldWorld = (long)Get(restarted, "worldEpoch");
            SceneManager.RaiseSceneLoaded(2);
            Require((long)Get(restarted, "worldEpoch") == oldWorld + 1,
                "scene load did not advance World generation");
            Invoke(restarted, "OnDestroy");

            if (File.Exists(path + ".bak")) File.Delete(path + ".bak");
            File.Move(path, path + ".bak");
            File.WriteAllText(path + ".tmp", "{\"schemaVersion\":999}");
            var recovered = new RinUnityWorkflow();
            Invoke(recovered, "Awake");
            Require((string)Get(recovered, "runId") == runId, "backup recovery changed identity");
            Invoke(recovered, "OnDestroy");

            var mismatched = new RinUnityWorkflow();
            Set(mismatched, "contentId", "other");
            Invoke(mismatched, "Awake");
            Require(!(bool)Get(mismatched, "authoritativeStateReady"),
                "changed content binding reused authoritative state");

            var blocked = Path.Combine(root, "blocked");
            File.WriteAllText(blocked, "not a directory");
            Application.persistentDataPath = blocked;
            var writeFailure = new RinUnityWorkflow();
            Invoke(writeFailure, "Awake");
            Require(!(bool)Get(writeFailure, "authoritativeStateReady"),
                "write failure published a new identity");
            Application.persistentDataPath = root;

            File.WriteAllText(path, "{\"schemaVersion\":999}");
            var malformed = new RinUnityWorkflow();
            Invoke(malformed, "Awake");
            Require(!(bool)Get(malformed, "authoritativeStateReady"), "malformed state was accepted");

            VerifyOpaqueActionArguments();
            VerifyIdentifierBoundaries();
            VerifyOfferBinding();
            VerifyDecisionWindowExpiry();
            VerifyActionGate();
            VerifyInterruptedRunRecovery();
            Console.WriteLine("Rin Unity workflow restart tests passed");
            return 0;
        }
        finally
        {
            Directory.Delete(root, true);
        }
    }

    private static void VerifyIdentifierBoundaries()
    {
        Require(
            RinUnityIds.IsValid("a" + new string('b', 95)),
            "96-character protocol identifier was rejected");
        Require(
            !RinUnityIds.IsValid("a" + new string('b', 96)),
            "97-character protocol identifier was accepted");
        Require(
            !RinUnityIds.IsValid("a" + new string('b', 127)),
            "128-character protocol identifier was accepted");
    }

    private static void VerifyOfferBinding()
    {
        // Host scenario: stale_epoch_rejection.
        var epoch = new Epoch
        {
            session_id = "unity.session.test",
            world_id = "unity.world",
            host = 1,
            world = 1,
            timeline = 1,
        };
        var window = Window(epoch);
        var authored = Offer(epoch, window.id);
        var selected = Offer(epoch.Copy(), window.id);
        selected.argumentsJson = "{}";
        Require(
            RinUnityOfferBinding.Matches(authored, selected),
            "opaque response arguments replaced durable offer authority");
        selected.descriptor_digest = new string('b', 64);
        Require(
            !RinUnityOfferBinding.Matches(authored, selected),
            "changed descriptor digest matched an authored offer");
        var changedWindow = Window(epoch);
        changedWindow.deadline = new Timepoint { clock = "step", value = 11 };
        Require(
            !RinUnityOfferBinding.DecisionWindowEquals(window, changedWindow),
            "changed Decision Window was accepted");
    }

    private static void VerifyActionGate()
    {
        // Host scenario: long_action_epoch_cancel.
        Action<RinHostActionResult> late = null;
        var handle = new TestAction(true);
        RinHostActionResult terminal = null;
        var terminalCount = 0;
        var gate = new RinUnityActionGate();
        gate.Begin(
            callback =>
            {
                late = callback;
                return handle;
            },
            result =>
            {
                terminal = result;
                terminalCount++;
            },
            work => work());
        gate.ReplaceAuthority("scene changed");
        Require(handle.CancelCalls == 1, "authority replacement did not cancel action");
        Require(terminal?.status == "cancelled", "known cancellation was not reported");
        late(new RinHostActionResult { accepted = true, status = "succeeded" });
        Require(terminalCount == 1, "late action callback revived a terminal run");

        var unknown = new RinUnityActionGate();
        RinHostActionResult unknownResult = null;
        unknown.Begin(
            callback => new TestAction(false),
            result => unknownResult = result,
            work => work());
        unknown.ReplaceAuthority("domain reloaded");
        Require(unknownResult?.status == "outcome-unknown",
            "uncertain cancellation was reported as known");
    }

    private static void VerifyDecisionWindowExpiry()
    {
        var epoch = new Epoch
        {
            session_id = "unity.session.test",
            world_id = "unity.world",
            host = 1,
            world = 1,
            timeline = 1,
        };
        var window = Window(epoch);
        var offer = Offer(epoch, window.id);
        var pending = new PendingTurnState
        {
            authoritative_clock = "step",
            authoritative_clock_value = 10,
        };
        var invocation = RinUnityOfferBinding.Invocation(
            "unity.operation.test",
            offer);
        Require(
            RinUnityClockAuthority.DeadlineAllowsStart(pending, invocation),
            "an unexpired Unity offer was rejected");
        pending.authoritative_clock_value = 11;
        Require(
            !RinUnityClockAuthority.DeadlineAllowsStart(pending, invocation),
            "an expired Unity offer reached action start");
        pending.authoritative_clock = "event";
        pending.authoritative_clock_value = 1;
        Require(
            !RinUnityClockAuthority.DeadlineAllowsStart(pending, invocation),
            "a mismatched authoritative clock reached action start");
    }

    private static void VerifyInterruptedRunRecovery()
    {
        var root = Path.Combine(
            Path.GetTempPath(),
            "rin-unity-active-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            Application.persistentDataPath = root;
            var workflow = new RinUnityWorkflow();
            Invoke(workflow, "Awake");
            var path = (string)Get(workflow, "statePath");
            Invoke(workflow, "OnDestroy");

            var state = JsonUtility.FromJson<DurableState>(File.ReadAllText(path));
            var epoch = new Epoch
            {
                session_id = "unity.session." + state.runId,
                world_id = "unity.world",
                host = state.hostEpoch,
                world = state.worldEpoch,
                timeline = state.timelineEpoch,
            };
            var window = Window(epoch);
            var offer = Offer(epoch, window.id);
            var operationId = "unity.operation." + state.runId + ".1";
            var proposal = new ActionProposal
            {
                id = "unity.proposal.test",
                session_id = epoch.session_id,
                request_id = "unity.propose." + state.runId + ".1",
                actor_id = "npc.guide",
                tick = 1,
                decision_window = window,
                action = offer,
            };
            var invocation = new ActionInvocation
            {
                operation_id = operationId,
                offer_id = offer.offer_id,
                decision_window_id = offer.decision_window_id,
                actor_id = offer.actor_id,
                capability = offer.capability,
                descriptor_digest = offer.descriptor_digest,
                argumentsJson = "{\"destination_id\":\"guide_marker\"}",
                targets = offer.targets,
                expected_epoch = epoch,
                observation_seq = 1,
                deadline = offer.deadline,
            };
            state.operationSequence = 1;
            state.observationSequence = 1;
            state.lastAuthoritativeTick = 1;
            state.pendingTurn = new PendingTurnState
            {
                version = 1,
                operation_id = operationId,
                authoritative_clock = "step",
                authoritative_clock_value = 1,
                offer_arguments_json = new[]
                {
                    "{\"destination_id\":\"guide_marker\"}",
                },
                observation = new ObserveRequest
                {
                    session_id = epoch.session_id,
                    request_id = "unity.observe." + state.runId + ".1",
                    event_id = "unity.event." + state.runId + ".1",
                    tick = 1,
                    epoch = epoch,
                    observation_seq = 1,
                },
                request = new ProposeRequest
                {
                    session_id = epoch.session_id,
                    request_id = proposal.request_id,
                    actor_id = proposal.actor_id,
                    tick = 1,
                    decision_window = window,
                    offers = new[] { offer },
                },
            };
            state.activeRun = new ActiveRunState
            {
                operation_id = operationId,
                arguments_json = invocation.argumentsJson,
                proposal = proposal,
                invocation = invocation,
            };
            state.applied = Array.Empty<AppliedMarker>();
            state.reportOutbox = Array.Empty<ReportOutboxEntry>();
            File.WriteAllText(path, JsonUtility.ToJson(state));

            var recovered = new RinUnityWorkflow();
            Invoke(recovered, "Awake");
            Require(GetNullable(recovered, "activeRun") == null,
                "domain reload retained an active process-local action");
            Require(GetNullable(recovered, "pendingTurn") == null,
                "domain reload retained a settled Pending Turn");
            var outbox = (System.Collections.IList)Get(recovered, "reportOutbox");
            Require(
                outbox.Count == 1,
                "recovery_state_cleanup: interrupted action did not enter the Outbox");
            var entry = (ReportOutboxEntry)outbox[0];
            Require(entry.request.report.outcome.status == "outcome-unknown",
                "interrupted action used the wrong terminal status");
            Require(entry.arguments_json == invocation.argumentsJson &&
                entry.request.report.invocation.argumentsJson == invocation.argumentsJson,
                "restart changed opaque invocation arguments");
            Require(
                !RinUnityReportValidation.Acknowledgement(
                    epoch.session_id,
                    entry,
                    new MutationResult { session_id = "unity.session.other" }),
                "cross-Session ACK removed a Unity Outcome");
            Require(
                !RinUnityReportValidation.Acknowledgement(
                    epoch.session_id,
                    entry,
                    new MutationResult()),
                "missing-Session ACK removed a Unity Outcome");
            Require(
                RinUnityReportValidation.Acknowledgement(
                    epoch.session_id,
                    entry,
                    new MutationResult { session_id = epoch.session_id }),
                "matching Unity Outcome ACK was rejected");
            Invoke(recovered, "OnDestroy");

            var corrupted = JsonUtility.FromJson<DurableState>(
                File.ReadAllText(path));
            corrupted.reportOutbox[0].arguments_json = "[]";
            File.WriteAllText(path, JsonUtility.ToJson(corrupted));
            var rejected = new RinUnityWorkflow();
            Invoke(rejected, "Awake");
            Require(!(bool)Get(rejected, "authoritativeStateReady"),
                "corrupted Outbox arguments were accepted");
        }
        finally
        {
            Directory.Delete(root, true);
        }
    }

    private static DecisionWindow Window(Epoch epoch) =>
        new DecisionWindow
        {
            id = "unity.window.test",
            mode = "sequential",
            epoch = epoch,
            observation_seq = 1,
            opened_at = new Timepoint { clock = "step", value = 1 },
            deadline = new Timepoint { clock = "step", value = 10 },
            actor_ids = new[] { "npc.guide" },
        };

    private static ActionOffer Offer(Epoch epoch, string windowId) =>
        new ActionOffer
        {
            offer_id = "unity.offer.move",
            decision_window_id = windowId,
            actor_id = "npc.guide",
            capability = new CapabilityRef
            {
                id = "movement.move_to",
                version = "1.0.0",
            },
            descriptor_digest = new string('a', 64),
            description = "Move to the authored marker.",
            argumentsJson = "{\"destination_id\":\"guide_marker\"}",
            expected_epoch = epoch,
            observation_seq = 1,
            deadline = new Timepoint { clock = "step", value = 10 },
        };

    private static void VerifyOpaqueActionArguments()
    {
        var epoch = new Epoch
        {
            session_id = "unity.session.test",
            world_id = "unity.world",
            host = 1,
            world = 1,
            timeline = 1,
        };
        var offer = new ActionOffer
        {
            offer_id = "unity.offer.test",
            decision_window_id = "unity.window.test",
            actor_id = "npc.guide",
            capability = new CapabilityRef
            {
                id = "dialogue.say",
                version = "1.0.0",
            },
            descriptor_digest = new string('a', 64),
            description = "Say an authored line.",
            argumentsJson = "{\"text\":\"hello\",\"volume\":2}",
            expected_epoch = epoch,
            observation_seq = 1,
            deadline = new Timepoint { clock = "step", value = 2 },
        };
        var request = new ProposeRequest
        {
            session_id = epoch.session_id,
            request_id = "unity.propose.test",
            actor_id = offer.actor_id,
            tick = 1,
            intent = "respond",
            decision_window = new DecisionWindow
            {
                id = offer.decision_window_id,
                mode = "sequential",
                epoch = epoch,
                observation_seq = 1,
                opened_at = new Timepoint { clock = "step", value = 1 },
                deadline = offer.deadline,
                actor_ids = new[] { offer.actor_id },
            },
            offers = new[] { offer },
        };
        var json = RinUnityJson.SerializePropose(request);
        using var document = JsonDocument.Parse(json);
        var arguments = document.RootElement
            .GetProperty("offers")[0]
            .GetProperty("arguments");
        Require(arguments.ValueKind == JsonValueKind.Object, "arguments became a JSON string");
        Require(arguments.GetProperty("text").GetString() == "hello",
            "opaque action arguments changed during serialization");
    }

    private static object Get(object target, string name) =>
        target.GetType().GetField(name, BindingFlags.Instance | BindingFlags.NonPublic)
            ?.GetValue(target) ?? throw new InvalidOperationException("missing field " + name);

    private static object GetNullable(object target, string name) =>
        target.GetType().GetField(name, BindingFlags.Instance | BindingFlags.NonPublic)
            ?.GetValue(target);

    private static void Set(object target, string name, object value) =>
        target.GetType().GetField(name, BindingFlags.Instance | BindingFlags.NonPublic)
            ?.SetValue(target, value);

    private static void Invoke(object target, string name) =>
        target.GetType().GetMethod(name, BindingFlags.Instance | BindingFlags.NonPublic)
            ?.Invoke(target, null);

    private static void Require(bool condition, string message)
    {
        if (!condition) throw new InvalidOperationException(message);
    }

    private sealed class TestAction : IRinUnityAction
    {
        private readonly bool known;
        public int CancelCalls { get; private set; }

        public TestAction(bool known)
        {
            this.known = known;
        }

        public bool Cancel()
        {
            CancelCalls++;
            return known;
        }
    }
}
