using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using BepInEx;
using BepInEx.Configuration;
using Rin.Client;
using UnityEngine;

namespace RinNpcExample;

[BepInPlugin(PluginGuid, PluginName, PluginVersion)]
public sealed class Plugin : BaseUnityPlugin
{
    public const string PluginGuid = "io.github.sunrioa.rin.npc-example";
    public const string PluginName = "Rin NPC Example";
    public const string PluginVersion = "0.1.0";

    private const string ActorId = "npc.rin.companion";
    private static readonly HashSet<string> AllowedActions = new(StringComparer.Ordinal)
    {
        "talk",
        "wait",
        "refuse",
    };

    private readonly ConcurrentQueue<Action> mainThread = new();
    private readonly SemaphoreSlim turnGate = new(1, 1);
    private readonly object sessionLock = new();
    private RinClient? rin;
    private ConfigEntry<string>? baseUrl;
    private ConfigEntry<bool>? demoHotkey;
    private Task? sessionTask;
    private string sessionId = string.Empty;
    private string gameId = string.Empty;
    private long sequence;

    public event Action<string, string>? NpcActionReady;

    private void Awake()
    {
        baseUrl = Config.Bind(
            "Connection",
            "BaseUrl",
            RinClient.DefaultBaseUrl,
            "Rin origin. Remote origins require HTTPS and RIN_TOKEN in the process environment.");
        demoHotkey = Config.Bind(
            "Example",
            "EnableF8Demo",
            true,
            "Press F8 to request one example NPC turn.");
        sessionId = "bepinex." + Guid.NewGuid().ToString("N");
        gameId = Application.productName;

        try
        {
            rin = new RinClient(new RinClientOptions
            {
                BaseUrl = baseUrl.Value,
                Token = Environment.GetEnvironmentVariable("RIN_TOKEN") ?? string.Empty,
            });
            Logger.LogInfo("Rin NPC example loaded. No network request runs until an interaction is triggered.");
        }
        catch (RinException exception)
        {
            Logger.LogError("Rin configuration rejected: " + exception.Code);
        }
    }

    private void Update()
    {
        for (var count = 0; count < 64 && mainThread.TryDequeue(out var action); count++)
        {
            action();
        }
        if (rin is not null && demoHotkey?.Value == true && Input.GetKeyDown(KeyCode.F8))
        {
            RequestNpcTurn("The player requested guidance from the companion.", Time.frameCount);
        }
    }

    private void OnDestroy()
    {
        rin?.Dispose();
        turnGate.Dispose();
    }

    public void RequestNpcTurn(string observation, long gameTick)
    {
        if (rin is null) return;
        _ = RunNpcTurnAsync(observation, gameTick);
    }

    private async Task RunNpcTurnAsync(string observation, long gameTick)
    {
        if (rin is null) return;
        await turnGate.WaitAsync().ConfigureAwait(false);
        try
        {
            await EnsureSessionAsync().ConfigureAwait(false);
            var turn = Interlocked.Increment(ref sequence);
            await rin.ObserveAsync(new Dictionary<string, object?>
            {
                ["protocol_version"] = RinClient.ProtocolVersion,
                ["session_id"] = sessionId,
                ["request_id"] = "observe." + turn,
                ["event_id"] = "event." + turn,
                ["tick"] = gameTick,
                ["observer_ids"] = new[] { ActorId },
                ["source"] = "bepinex-example",
                ["kind"] = "dialogue",
                ["summary"] = observation,
                ["tags"] = new[] { "conversation", "player-request" },
                ["importance"] = 3,
            }).ConfigureAwait(false);

            var queued = await rin.SubmitProposalJobAsync(new Dictionary<string, object?>
            {
                ["protocol_version"] = RinClient.ProtocolVersion,
                ["session_id"] = sessionId,
                ["request_id"] = "propose." + turn,
                ["actor_id"] = ActorId,
                ["tick"] = gameTick + 1,
                ["intent"] = "Choose one bounded response to the player.",
                ["tags"] = new[] { "conversation" },
                ["candidate_actions"] = new object[]
                {
                    ActionSpec("talk", "dialogue", "offer one concrete hint"),
                    ActionSpec("wait", "wait", "ask the player to observe first"),
                    ActionSpec("refuse", "refuse", "decline an unsafe request"),
                },
            }).ConfigureAwait(false);
            var jobId = RequiredString(queued, "job_id");
            var job = await rin.WaitForProposalAsync(jobId).ConfigureAwait(false);
            var applied = await ApplyOnMainThreadAsync(job).ConfigureAwait(false);

            var proposal = RequiredObject(job, "proposal");
            await rin.CommitAsync(new Dictionary<string, object?>
            {
                ["protocol_version"] = RinClient.ProtocolVersion,
                ["session_id"] = sessionId,
                ["request_id"] = "commit." + turn,
                ["proposal_id"] = RequiredString(proposal, "proposal_id"),
                ["event_id"] = "outcome." + turn,
                ["tick"] = gameTick + 2,
                ["accepted"] = applied.Accepted,
                ["outcome"] = applied.Outcome,
                ["tags"] = new[] { "bepinex-example", "conversation" },
            }).ConfigureAwait(false);
            EnqueueLog("Rin turn committed.");
        }
        catch (RinException exception)
        {
            EnqueueLog("Rin request failed: " + exception.Code, error: true);
        }
        catch (Exception)
        {
            EnqueueLog("Rin integration failed before the proposal could be applied.", error: true);
        }
        finally
        {
            turnGate.Release();
        }
    }

    private Task EnsureSessionAsync()
    {
        lock (sessionLock)
        {
            return sessionTask ??= CreateSessionAsync();
        }
    }

    private async Task CreateSessionAsync()
    {
        if (rin is null) throw new InvalidOperationException("Rin is not configured");
        try
        {
            await rin.CreateSessionAsync(new Dictionary<string, object?>
            {
                ["protocol_version"] = RinClient.ProtocolVersion,
                ["request_id"] = "create." + sessionId,
                ["session_id"] = sessionId,
                ["binding"] = new Dictionary<string, object?>
                {
                    ["game_id"] = gameId,
                    ["content_id"] = "rin-bepinex-example",
                    ["content_version"] = PluginVersion,
                    ["content_hash"] = "sha256:" + new string('0', 64),
                },
                ["seed"] = DateTimeOffset.UtcNow.ToUnixTimeSeconds(),
                ["actors"] = new object[]
                {
                    new Dictionary<string, object?>
                    {
                        ["id"] = ActorId,
                        ["kind"] = "npc",
                        ["display_name"] = "Rin Companion",
                        ["traits"] = new[] { "observant", "careful" },
                        ["boundaries"] = new object[]
                        {
                            new Dictionary<string, object?>
                            {
                                ["id"] = "boundary.no-cheats",
                                ["description"] = "Never suggest cheats or bypassing game rules.",
                                ["trigger_tags"] = new[] { "unsafe" },
                                ["response"] = "refuse",
                            },
                        },
                        ["goals"] = new object[]
                        {
                            new Dictionary<string, object?>
                            {
                                ["id"] = "goal.help-player",
                                ["description"] = "Help the player make one informed choice.",
                                ["priority"] = 4,
                                ["preferred_actions"] = new[] { "talk" },
                                ["progress"] = 0,
                                ["target_progress"] = 3,
                                ["status"] = "active",
                            },
                        },
                        ["think_every_ticks"] = 20,
                        ["enabled"] = true,
                    },
                },
            }).ConfigureAwait(false);
        }
        catch
        {
            lock (sessionLock) sessionTask = null;
            throw;
        }
    }

    private Task<AppliedAction> ApplyOnMainThreadAsync(JsonElement job)
    {
        var completion = new TaskCompletionSource<AppliedAction>(TaskCreationOptions.RunContinuationsAsynchronously);
        mainThread.Enqueue(() ->
        {
            try
            {
                var proposal = RequiredObject(job, "proposal");
                var action = RequiredObject(proposal, "action");
                var actionId = RequiredString(action, "id");
                if (!AllowedActions.Contains(actionId))
                {
                    completion.SetResult(new AppliedAction(false, "The game rejected an action outside its allowlist."));
                    return;
                }
                var line = actionId switch
                {
                    "talk" => "Companion: Check your resources before choosing the next route.",
                    "wait" => "Companion: Let us observe one more cycle before acting.",
                    "refuse" => "Companion: I cannot help with an action that breaks the game rules.",
                    _ => throw new InvalidOperationException("allowlist changed during apply"),
                };
                Logger.LogMessage(line);
                NpcActionReady?.Invoke(actionId, line);
                completion.SetResult(new AppliedAction(true, line));
            }
            catch (Exception)
            {
                completion.SetResult(new AppliedAction(false, "The game could not apply the proposal."));
            }
        });
        return completion.Task;
    }

    private void EnqueueLog(string message, bool error = false)
    {
        mainThread.Enqueue(() =>
        {
            if (error) Logger.LogError(message); else Logger.LogInfo(message);
        });
    }

    private static Dictionary<string, object?> ActionSpec(string id, string kind, string description) => new()
    {
        ["id"] = id,
        ["kind"] = kind,
        ["description"] = description,
    };

    private static JsonElement RequiredObject(JsonElement parent, string name)
    {
        if (!parent.TryGetProperty(name, out var value) || value.ValueKind != JsonValueKind.Object)
            throw new RinProtocolException("invalid_response", "Rin response is missing " + name);
        return value;
    }

    private static string RequiredString(JsonElement parent, string name)
    {
        if (!parent.TryGetProperty(name, out var value) || value.ValueKind != JsonValueKind.String)
            throw new RinProtocolException("invalid_response", "Rin response is missing " + name);
        return value.GetString() ?? string.Empty;
    }

    private sealed class AppliedAction
    {
        public AppliedAction(bool accepted, string outcome)
        {
            Accepted = accepted;
            Outcome = outcome;
        }

        public bool Accepted { get; }
        public string Outcome { get; }
    }
}
