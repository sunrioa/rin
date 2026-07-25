using BepInEx;
using BepInEx.Unity.IL2CPP;

namespace RinNpcExample.IL2CPP;

[BepInPlugin(PluginGuid, PluginName, PluginVersion)]
public sealed class Plugin : BasePlugin, IRinNpcHost
{
    public const string PluginGuid = "io.github.sunrioa.rin.npc-example.il2cpp";
    public const string PluginName = "Rin NPC Example (IL2CPP)";
    public const string PluginVersion = "0.6.0";

    private RinNpcRuntime? runtime;
    private readonly CancellationTokenSource shutdown = new();
    private long gameTick;

    public long CurrentTick => Interlocked.Read(ref gameTick);

    /// <summary>
    /// A game-specific IL2CPP hook must set this to an implementation that
    /// marshals to the game's owning thread. Generated interop assemblies are
    /// intentionally not guessed by this generic package.
    /// </summary>
    public Func<string, string, CancellationToken, Task<bool>>? ApplyDialogue { get; set; }

    public override void Load()
    {
        var baseUrl = Config.Bind(
            "Connection", "BaseUrl", Rin.Client.RinClient.DefaultBaseUrl,
            "Rin origin; remote origins require HTTPS and RIN_TOKEN.");
        var saveIdentity = Config.Bind(
            "Identity", "SaveIdentity", "demo",
            "Stable game save/profile identity. Set this from the real save slot.");
        var productIdentity = Config.Bind(
            "Identity", "ProductIdentity", PluginGuid,
            "Stable game/product identity. Do not derive it from an executable path.");
        var state = BepInExWorkflowState.Open(
            Path.Combine(Paths.ConfigPath, "rin-npc-example"),
            productIdentity.Value,
            saveIdentity.Value);
        runtime = new RinNpcRuntime(
            this,
            state,
            baseUrl.Value,
            Environment.GetEnvironmentVariable("RIN_TOKEN") ?? string.Empty);
        Log.LogInfo("Rin IL2CPP transport loaded; register the game-specific main-thread hook.");
        Log.LogInfo("Rin diagnostics: " + state.Diagnostics);
    }

    public async Task RequestNpcTurnAsync(
        string observation,
        long authoritativeTick,
        CancellationToken cancellationToken = default)
    {
        Interlocked.Exchange(ref gameTick, authoritativeTick);
        if (runtime is null) return;
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(
            cancellationToken,
            shutdown.Token);
        await runtime.RequestTurnAsync(
            observation,
            authoritativeTick,
            linked.Token).ConfigureAwait(false);
    }

    public override bool Unload()
    {
        shutdown.Cancel();
        runtime?.Dispose();
        shutdown.Dispose();
        return true;
    }

    public Task<bool> ApplyDialogueAsync(
        string actionId,
        string authoredLine,
        CancellationToken cancellationToken)
    {
        var apply = ApplyDialogue ?? throw new InvalidOperationException(
            "The IL2CPP game hook did not register an owning-thread apply delegate");
        return apply(actionId, authoredLine, cancellationToken);
    }

    void IRinNpcHost.Log(string message, bool error)
    {
        if (error) Log.LogError(message);
        else Log.LogInfo(message);
    }
}
