using System;
using System.IO;
using System.Reflection;
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

            Console.WriteLine("Rin Unity workflow restart tests passed");
            return 0;
        }
        finally
        {
            Directory.Delete(root, true);
        }
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
