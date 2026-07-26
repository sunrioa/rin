using System.Collections.Concurrent;
using BepInEx;
using BepInEx.Configuration;
using BepInEx.Unity.Mono;
using UnityEngine;

namespace RinNpcExample.Mono;

[BepInPlugin(PluginGuid, PluginName, PluginVersion)]
public sealed class Plugin : BaseUnityPlugin, IRinNpcHost
{
    public const string PluginGuid = "io.github.sunrioa.rin.npc-example.mono";
    public const string PluginName = "Rin NPC Example (Mono)";
    public const string PluginVersion = "0.7.0";

    private readonly ConcurrentQueue<Action> mainThread = new();
    private readonly CancellationTokenSource shutdown = new();
    private RinNpcRuntime? runtime;
    private ConfigEntry<bool>? demoHotkey;

    public long CurrentTick => Time.frameCount;

    private void Awake()
    {
        var baseUrl = Config.Bind(
            "Connection", "BaseUrl", Rin.Client.RinClient.DefaultBaseUrl,
            "Rin origin; remote origins require HTTPS and RIN_TOKEN.");
        var saveIdentity = Config.Bind(
            "Identity", "SaveIdentity", "demo",
            "Stable game save/profile identity. Set this from the real save slot.");
        var productIdentity = Config.Bind(
            "Identity", "ProductIdentity", Application.productName,
            "Stable game/product identity. Do not derive it from an executable path.");
        demoHotkey = Config.Bind(
            "Example", "EnableF8Demo", true,
            "Press F8 to request one example NPC turn.");
        var state = BepInExWorkflowState.Open(
            Path.Combine(Paths.ConfigPath, "rin-npc-example"),
            productIdentity.Value,
            saveIdentity.Value);
        runtime = new RinNpcRuntime(
            this,
            state,
            baseUrl.Value,
            Environment.GetEnvironmentVariable("RIN_TOKEN") ?? string.Empty);
        Logger.LogInfo("Rin Mono example loaded for save identity " + saveIdentity.Value);
        Logger.LogInfo("Rin diagnostics: " + state.Diagnostics);
    }

    private void Update()
    {
        for (var count = 0; count < 64 && mainThread.TryDequeue(out var action); count++)
        {
            action();
        }
        if (runtime is not null && demoHotkey?.Value == true &&
            Input.GetKeyDown(KeyCode.F8))
        {
            _ = runtime.RequestTurnAsync(
                "The player requested guidance from the companion.",
                Time.frameCount,
                shutdown.Token);
        }
    }

    private void OnDestroy()
    {
        shutdown.Cancel();
        runtime?.Dispose();
        shutdown.Dispose();
    }

    public async Task<bool> ApplyDialogueAsync(
        string actionId,
        string authoredLine,
        CancellationToken cancellationToken)
    {
        var completion = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        mainThread.Enqueue(() =>
        {
            try
            {
                cancellationToken.ThrowIfCancellationRequested();
                Logger.LogInfo(authoredLine);
                completion.TrySetResult(true);
            }
            catch (Exception exception)
            {
                completion.TrySetException(exception);
            }
        });
        using (cancellationToken.Register(() => completion.TrySetCanceled()))
        {
            return await completion.Task.ConfigureAwait(false);
        }
    }

    public void Log(string message, bool error = false)
    {
        mainThread.Enqueue(() =>
        {
            if (error) Logger.LogError(message);
            else Logger.LogInfo(message);
        });
    }
}
