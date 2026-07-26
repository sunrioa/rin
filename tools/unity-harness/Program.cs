using System;
using System.IO;
using System.Reflection;
using System.Text.Json;
using UnityEngine;

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
            Require(File.Exists(path), "initialized state was not persisted");

            var restarted = new RinUnityWorkflow();
            Invoke(restarted, "Awake");
            Require((string)Get(restarted, "runId") == runId, "restart minted a new identity");

            File.Move(path, path + ".bak");
            File.WriteAllText(path + ".tmp", "{\"schemaVersion\":999}");
            var recovered = new RinUnityWorkflow();
            Invoke(recovered, "Awake");
            Require((string)Get(recovered, "runId") == runId, "backup recovery changed identity");

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
            Console.WriteLine("Rin Unity workflow restart tests passed");
            return 0;
        }
        finally
        {
            Directory.Delete(root, true);
        }
    }

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

    private static void Invoke(object target, string name) =>
        target.GetType().GetMethod(name, BindingFlags.Instance | BindingFlags.NonPublic)
            ?.Invoke(target, null);

    private static void Require(bool condition, string message)
    {
        if (!condition) throw new InvalidOperationException(message);
    }
}
