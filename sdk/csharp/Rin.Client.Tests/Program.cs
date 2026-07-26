using System.Net;
using System.Text;
using System.Text.Json;
using Rin.Client;

Require(
    new RinClientOptions().MaxResponseBytes == 32 * 1024 * 1024,
    "default response limit does not match the inline transport budget");
Require(RinClient.ClientVersion == "0.7.0", "client version projection is stale");
var stableId = RinIds.Create("report");
Require(
    stableId.StartsWith("report.", StringComparison.Ordinal) &&
    stableId.Length == "report.".Length + 32,
    "stable ID helper did not produce a protocol-safe identifier");
Require(
    RinIds.IsValid("a" + new string('b', 95)) &&
    !RinIds.IsValid("a" + new string('b', 96)),
    "protocol identifier validation does not enforce the 96-character boundary");
try
{
    RinIds.Create("bad prefix");
    throw new InvalidOperationException("invalid ID prefix was accepted");
}
catch (RinConfigurationException exception)
{
    Require(exception.Code == "invalid_id_prefix", "invalid ID prefix code changed");
}

var helperWindow = TestWindow("session.helper", "actor.helper", 7);
var helperOffer = HostActions.Offer(
    "offer.helper",
    "actor.helper",
    new CapabilityRef("dialogue.say", "1"),
    new string('a', 64),
    "Say one line",
    helperWindow,
    JsonSerializer.SerializeToElement(new { line = "hello" }));
var helperProposal = new ActionProposal(
    "proposal.helper",
    "session.helper",
    "request.helper",
    "actor.helper",
    7,
    1,
    string.Empty,
    2,
    helperWindow,
    helperOffer,
    "engage",
    "Say one line",
    "Useful",
    "pending");
var helperReport = HostActions.ImmediateReport(
    "session.helper",
    "report.helper",
    "event.helper",
    8,
    helperProposal,
    "operation.helper",
    true,
    "applied",
    helperProposal.DecisionWindow.Epoch,
    8,
    new Timepoint("event", 8));
Require(
    helperReport.Report.Invocation?.OfferId == helperProposal.Action.OfferId &&
    helperReport.Report.Run?.Status == "succeeded" &&
    helperReport.Report.Outcome?.OperationId == "operation.helper",
    "host action helper did not preserve the selected offer lifecycle");
var rejectedHelperReport = HostActions.ImmediateReport(
    "session.helper",
    "report.rejected",
    "event.rejected",
    9,
    helperProposal,
    "operation.rejected",
    false,
    "rejected",
    helperProposal.DecisionWindow.Epoch,
    9,
    new Timepoint("event", 9));
Require(
    rejectedHelperReport.Report.Invocation is null &&
    rejectedHelperReport.Report.Run is null &&
    rejectedHelperReport.Report.Outcome is null,
    "rejected host action incorrectly emitted an execution lifecycle");

var capabilityHandler = new RecordingHandler
{
    ResponseBodyFactory = _ => JsonSerializer.Serialize(new
    {
        ok = true,
        data = new
        {
            status = "ok",
            protocol_version = RinClient.ProtocolVersion,
            release_version = RinClient.ClientVersion,
            release_status = "preview",
            policy_mode = "deterministic",
            async_jobs = true,
            structured_generation = true,
            features = RinFeatures.FullPreset,
            recommended_features = RinFeatures.SafeBaselinePreset,
        },
    }),
};
using (var capabilityClient = new RinClient(new RinClientOptions(), capabilityHandler))
{
    var capabilities = await capabilityClient.NegotiateCapabilitiesAsync();
    Require(
        capabilities.RecommendedFeatures.Count == 0,
        "protocol v2 should not require an optional feature flag");
}

var typedMutationHandler = new RecordingHandler
{
    ResponseBodyFactory = _ =>
        "{\"ok\":true,\"data\":{\"session_id\":\"session.fixture\"," +
        "\"revision\":3,\"head_hash\":\"" + new string('a', 64) +
        "\",\"duplicate\":false,\"future_field\":\"preserved\"}}",
};
using (var typedClient = new RinClient(new RinClientOptions(), typedMutationHandler))
{
    var create = new CreateSessionRequest(
        "create.fixture",
        "session.fixture",
        new RinBinding("game.fixture", "base", "1", "hash"),
        new[]
        {
            new ActorSeedInput("actor.fixture", "npc", "Fixture", 5)
            {
                Enabled = true,
            },
        })
    {
        Features = RinFeatures.AuthoritativePreset,
    };
    var created = await typedClient.CreateSessionAsync(create);
    Require(created.Revision == 3, "typed mutation response lost revision");
    Require(
        created.AdditiveFields?.ContainsKey("future_field") == true,
        "typed mutation response discarded an additive field");
    using var sent = JsonDocument.Parse(typedMutationHandler.Body);
    Require(
        sent.RootElement.GetProperty("binding").GetProperty("game_id").GetString() ==
        "game.fixture",
        "typed Binding did not use OpenAPI property names");
    Require(
        sent.RootElement.GetProperty("actors")[0].GetProperty("display_name").GetString() ==
        "Fixture",
        "typed actor did not use OpenAPI property names");

    var reported = await typedClient.ReportActionAsync(
        RejectedReport("session.fixture", "report.fixture", "proposal.fixture", "event.fixture"));
    Require(reported.Revision == 3, "typed report response was not decoded");
    using var reportBody = JsonDocument.Parse(typedMutationHandler.Body);
    Require(
        reportBody.RootElement.GetProperty("report").GetProperty("decision").GetString() ==
        "rejected",
        "typed report omitted the host decision");
}

var typedProposalHandler = new RecordingHandler
{
    ResponseBodyFactory = _ => JsonSerializer.Serialize(new
    {
        ok = true,
        data = new
        {
            duplicate = false,
            future = "ok",
            proposal = TestProposal(
                "proposal.fixture",
                "session.fixture",
                "propose.fixture",
                "actor.fixture",
                4),
        },
    }),
};
using (var typedProposalClient = new RinClient(
    new RinClientOptions(),
    typedProposalHandler))
{
    var result = await typedProposalClient.ProposeAsync(
        TestProposeRequest(
            "session.fixture",
            "propose.fixture",
            "actor.fixture",
            4));
    Require(result.Proposal.Id == "proposal.fixture", "typed Proposal was not decoded");
    Require(
        result.AdditiveFields?.ContainsKey("future") == true,
        "typed Proposal result discarded an additive field");
    using var freshState = JsonDocument.Parse(
        "{\"revision\":2,\"proposals\":{\"proposal.fixture\":{\"status\":\"pending\"}}}");
    Require(
        ProposalFreshness.Evaluate(freshState.RootElement, result.Proposal) ==
        ProposalFreshnessDecision.Fresh,
        "fresh Session Proposal was rejected");
    using var malformedState = JsonDocument.Parse(
        "{\"revision\":\"2\",\"proposals\":{\"proposal.fixture\":{\"status\":7}}}");
    Require(
        ProposalFreshness.Evaluate(malformedState.RootElement, result.Proposal) ==
        ProposalFreshnessDecision.Stale,
        "malformed freshness fields were not fail-closed");
    using var worldState = JsonDocument.Parse(
        "{\"world_revision\":4,\"proposals\":{\"proposal.fixture\":{\"status\":\"pending\"}}}");
    Require(
        ProposalFreshness.Evaluate(
            worldState.RootElement,
            result.Proposal with { BasedOnWorldRevision = 4 }) ==
        ProposalFreshnessDecision.Fresh,
        "fresh world Proposal was rejected");
}

var workflowHandler = new RecordingHandler
{
    ResponseBodyFactory = request =>
    {
        var path = request.RequestUri?.AbsolutePath;
        if (path == "/v2/jobs/propose")
        {
            return "{\"ok\":true,\"data\":{\"protocol_version\":\"rin.protocol/v2\"," +
                "\"job_id\":\"job.workflow\",\"status\":\"queued\",\"duplicate\":false}}";
        }
        if (path == "/v2/jobs/job.workflow")
        {
            return JsonSerializer.Serialize(new
            {
                ok = true,
                data = new
                {
                    protocol_version = RinClient.ProtocolVersion,
                    job_id = "job.workflow",
                    session_id = "session.workflow",
                    request_id = "request.workflow",
                    status = "succeeded",
                    submitted_at = "2026-01-01T00:00:00Z",
                    duplicate = false,
                    proposal = TestProposal(
                        "proposal.workflow",
                        "session.workflow",
                        "request.workflow",
                        "actor.workflow",
                        5),
                },
            });
        }
        return "{\"ok\":true,\"data\":{\"session_id\":\"session.workflow\"," +
            "\"revision\":3,\"head_hash\":\"" + new string('a', 64) +
            "\",\"duplicate\":true}}";
    },
};
using (var workflowClient = new RinClient(new RinClientOptions(), workflowHandler))
{
    var workflowStore = new TestAuthoritativeStore();
    var coordinator = new ProposalAttemptCoordinator(workflowClient, workflowStore);
    var proposeRequest = TestProposeRequest(
        "session.workflow",
        "request.workflow",
        "actor.workflow",
        5);
    await coordinator.BeginAsync("operation.workflow", proposeRequest);
    var resolved = await coordinator.ResumeAsync(
        TimeSpan.FromMilliseconds(100),
        TimeSpan.FromMilliseconds(10));
    Require(
        workflowStore.Attempt?.JobId == "job.workflow",
        "Proposal job identity was not persisted before polling");
    var applied = 0;
    await coordinator.SettleAsync(
        resolved.Attempt,
        resolved.Proposal,
        RejectedReport(
            "session.workflow",
            "report.workflow",
            "proposal.workflow",
            "event.workflow"),
        _ =>
        {
            applied++;
            return ValueTask.CompletedTask;
        });
    Require(applied == 1, "authoritative apply callback did not run");
    Require(workflowStore.Attempt is null, "settled Proposal Attempt was retained");
    Require(workflowStore.Outcomes.Count == 1, "settlement did not create Outbox entry");

    var outbox = new OutcomeOutbox(workflowClient, workflowStore);
    Require(await outbox.DrainAsync() == 1, "Outcome Outbox did not drain");
    Require(workflowStore.Outcomes.Count == 0, "acknowledged Outcome was retained");

    try
    {
        _ = new HostDurability(
            1,
            HostDurabilityProfile.TransactionalAction,
            false,
            false,
            false,
            false,
            false).Validate();
        throw new InvalidOperationException("inflated host durability claim was accepted");
    }
    catch (RinConfigurationException exception)
    {
        Require(
            exception.Code == "invalid_host_durability",
            "invalid host durability returned the wrong code");
    }

    var idempotentStore = new TestAuthoritativeStore();
    var workflow = new WorkflowCoordinator(
        workflowClient,
        idempotentStore,
        HostDurability.IdempotentAction());
    var pendingTurn = await workflow.BeginAsync("operation.workflow", proposeRequest);
    var appliedOperation = "";
    await workflow.ApplyAndEnqueueOutcomeAsync(
        pendingTurn,
        resolved.Proposal,
        RejectedReport(
            "session.workflow",
            "report.workflow",
            "proposal.workflow",
            "event.workflow"),
        HostDurabilityProfile.IdempotentAction,
        (operationId, _) =>
        {
            appliedOperation = operationId;
            return ValueTask.CompletedTask;
        });
    Require(
        appliedOperation == "operation.workflow",
        "idempotent apply did not receive the stable operation ID");
    Require(idempotentStore.Attempt is null, "completed Pending Turn was retained");
    Require(idempotentStore.Outcomes.Count == 1, "completed Pending Turn was not enqueued");
    Require(await workflow.DrainOutboxAsync() == 1, "Workflow Outbox did not drain");

    var failedRequest = proposeRequest with { RequestId = "request.failed" };
    var failedTurn = await workflow.BeginAsync("operation.failed", failedRequest);
    try
    {
        await workflow.ApplyAndEnqueueOutcomeAsync(
            failedTurn,
            resolved.Proposal with { RequestId = "request.failed" },
            RejectedReport(
                "session.workflow",
                "report.failed",
                "proposal.workflow",
                "event.failed"),
            HostDurabilityProfile.IdempotentAction,
            (_, _) => throw new InvalidOperationException("game save failed"));
        throw new InvalidOperationException("failed apply was accepted");
    }
    catch (InvalidOperationException exception)
    {
        Require(exception.Message == "game save failed", "failed apply changed error");
    }
    Require(
        idempotentStore.Attempt?.OperationId == "operation.failed",
        "failed apply removed the Pending Turn");
    Require(idempotentStore.Outcomes.Count == 0, "failed apply enqueued an Outcome");

}

var opaqueStore = new TestOpaqueSnapshotStore();
var opaquePersistence = new OpaqueSnapshotPersistence(opaqueStore);
await opaquePersistence.SaveAsync(
    "slot.workflow",
    new Dictionary<string, object?>
    {
        ["protocol_version"] = RinClient.ProtocolVersion,
        ["state_hash"] = new string('a', 64),
        ["future_additive"] = new Dictionary<string, object?>
        {
            ["nested"] = new object?[] { "preserved", 7 },
        },
    });
var opaqueSnapshot = await opaquePersistence.LoadAsync("slot.workflow");
Require(
    opaqueSnapshot.GetProperty("future_additive")
        .GetProperty("nested")[0].GetString() == "preserved",
    "opaque Snapshot persistence discarded an additive field");

var handler = new RecordingHandler();
using var client = new RinClient(new RinClientOptions { Token = "fixture" }, handler);
var payload = new Dictionary<string, object?>
{
    ["protocol_version"] = RinClient.ProtocolVersion,
    ["request_id"] = "request.fixture",
    ["utf8"] = "雨",
};
var transferOutput = new MemoryStream();
var transferInput = new MemoryStream(Encoding.UTF8.GetBytes(TransferFixture.Body()));
var transferBinding = new RinBinding(
    "game.fixture",
    "base",
    "1",
    "hash");
var cases = new (string Name, Func<Task<JsonElement>> Call, HttpMethod Method, string Path)[]
{
    ("health", () => client.HealthAsync(), HttpMethod.Get, "/health"),
    ("create_session", () => client.CreateSessionAsync(payload), HttpMethod.Post, "/v2/session/create"),
    ("observe", () => client.ObserveAsync(payload), HttpMethod.Post, "/v2/session/observe"),
    ("propose", () => client.ProposeAsync(payload), HttpMethod.Post, "/v2/agent/propose"),
    ("submit_proposal_job", () => client.SubmitProposalJobAsync(payload), HttpMethod.Post, "/v2/jobs/propose"),
    ("get_proposal_job", () => client.GetProposalJobAsync("job.fixture"), HttpMethod.Get, "/v2/jobs/job.fixture"),
    ("cancel_proposal_job", () => client.CancelProposalJobAsync("job.fixture"), HttpMethod.Delete, "/v2/jobs/job.fixture"),
    ("submit_generation_job", () => client.SubmitGenerationJobAsync(payload), HttpMethod.Post, "/v2/generation/jobs"),
    ("get_generation_job", () => client.GetGenerationJobAsync("job.fixture"), HttpMethod.Get, "/v2/generation/jobs/job.fixture"),
    ("cancel_generation_job", () => client.CancelGenerationJobAsync("job.fixture"), HttpMethod.Delete, "/v2/generation/jobs/job.fixture"),
    ("report_action", () => client.ReportActionAsync(payload), HttpMethod.Post, "/v2/action/report"),
    ("report_action_batch", () => client.ReportActionBatchAsync(payload), HttpMethod.Post, "/v2/action/report-batch"),
    ("set_actor_activity", () => client.SetActorActivityAsync(payload), HttpMethod.Post, "/v2/session/activity"),
    ("arbitrate", () => client.ArbitrateAsync(payload), HttpMethod.Post, "/v2/world/arbitrate"),
    ("state", () => client.StateAsync(payload), HttpMethod.Post, "/v2/session/get"),
    ("session_stats", () => client.SessionStatsAsync(payload), HttpMethod.Post, "/v2/session/stats"),
    ("archive_session", () => client.ArchiveSessionAsync(payload), HttpMethod.Post, "/v2/session/archive"),
    ("delete_session", () => client.DeleteSessionAsync(payload), HttpMethod.Post, "/v2/session/delete"),
    ("snapshot", () => client.SnapshotAsync(payload), HttpMethod.Post, "/v2/session/snapshot"),
    ("restore", () => client.RestoreAsync(payload), HttpMethod.Post, "/v2/session/restore"),
    ("export_session", () => client.ExportSessionAsync(payload, transferOutput), HttpMethod.Post, "/v2/session/export"),
    ("import_session", () => client.ImportSessionAsync(transferInput, transferBinding), HttpMethod.Post, "/v2/session/import"),
    ("timeline", () => client.TimelineAsync(payload), HttpMethod.Post, "/v2/session/timeline"),
    ("replay", () => client.ReplayAsync(payload), HttpMethod.Post, "/v2/session/replay"),
    ("due_agents", () => client.DueAgentsAsync(payload), HttpMethod.Post, "/v2/scheduler/due"),
};

var observedRoutes = new List<string>();
foreach (var test in cases)
{
    var result = await test.Call();
    Require(handler.Method == test.Method, "wrong method for " + test.Path);
    Require(handler.Path == test.Path, "wrong path for " + test.Path);
    Require(handler.Authorization == "Bearer fixture", "missing bearer token");
    Require(handler.UserAgent == "rin-csharp/" + RinClient.ClientVersion, "wrong user agent");
    if (test.Path == "/v2/session/export")
    {
        Require(
            result.GetProperty("type").GetString() == "complete",
            "transfer complete frame was not returned");
    }
    else
    {
        Require(result.GetProperty("status").GetString() == "ok", "response envelope was not decoded");
    }
    if (test.Path == "/v2/session/import")
    {
        Require(
            handler.ExpectedGameId == transferBinding.GameId,
            "trusted transfer Binding header was not sent");
        Require(
            handler.ContentType == "application/x-ndjson",
            "transfer import media type changed");
    }
    else if (test.Method == HttpMethod.Post)
    {
        using var sent = JsonDocument.Parse(handler.Body);
        Require(
            sent.RootElement.GetProperty("protocol_version").GetString() == RinClient.ProtocolVersion,
            "request protocol_version was not serialized");
        Require(
            sent.RootElement.GetProperty("request_id").GetString() == "request.fixture",
            "request_id was not serialized");
        Require(sent.RootElement.GetProperty("utf8").GetString() == "雨", "UTF-8 request text changed");
    }
    else
    {
        Require(handler.Body == string.Empty, "bodyless route sent a request body");
    }
    observedRoutes.Add(RouteKey(
        test.Name,
        handler.Method?.Method ?? string.Empty,
        handler.Path.Replace("job.fixture", "{job_id}"),
        (int)handler.Status));
}
var expectedRoutes = ContractRouteKeys();
observedRoutes.Sort(StringComparer.Ordinal);
Require(
    observedRoutes.SequenceEqual(expectedRoutes, StringComparer.Ordinal),
    "actual SDK request method/path/status set differs from sdk/conformance/routes.json");
Require(
    Encoding.UTF8.GetString(transferOutput.ToArray()) == TransferFixture.Body(),
    "transfer export destination did not receive the exact framed stream");
Require(transferInput.CanRead, "transfer import closed the caller-owned source");
Require(transferOutput.CanWrite, "transfer export closed the caller-owned destination");

var transferLines = TransferFixture.Body().Split('\n');
var transferErrorHandler = new RecordingHandler
{
    TransferResponseBody = transferLines[0] + "\n" +
        "{\"type\":\"error\",\"error\":{\"code\":\"store_load_failed\"," +
        "\"message\":\"export stopped\"}}\n",
};
using (var transferErrorClient = new RinClient(
    new RinClientOptions(),
    transferErrorHandler))
{
    try
    {
        await transferErrorClient.ExportSessionAsync(payload, new MemoryStream());
        throw new InvalidOperationException("terminal transfer error was accepted");
    }
    catch (RinApiException exception)
    {
        Require(
            exception.Code == "store_load_failed",
            "terminal transfer error code changed");
    }
}

var truncatedTransferHandler = new RecordingHandler
{
    TransferResponseBody = transferLines[0] + "\n" + transferLines[1] + "\n",
};
using (var truncatedTransferClient = new RinClient(
    new RinClientOptions(),
    truncatedTransferHandler))
{
    try
    {
        await truncatedTransferClient.ExportSessionAsync(payload, new MemoryStream());
        throw new InvalidOperationException("truncated transfer was accepted");
    }
    catch (RinProtocolException exception)
    {
        Require(
            exception.Code == "invalid_transfer",
            "truncated transfer returned the wrong error");
    }
}

await client.ReportActionAsync(new Dictionary<string, object?>
{
    ["report"] = new Dictionary<string, object?> { ["decision"] = "rejected" },
});
using (var sent = JsonDocument.Parse(handler.Body))
{
    Require(
        sent.RootElement.GetProperty("report").GetProperty("decision").GetString() ==
        "rejected",
        "report decision was omitted or changed");
}
await client.ReportActionBatchAsync(new Dictionary<string, object?>
{
    ["reports"] = new object?[]
    {
        new Dictionary<string, object?> { ["decision"] = "rejected" },
    },
});
using (var sent = JsonDocument.Parse(handler.Body))
{
    var item = sent.RootElement.GetProperty("reports").EnumerateArray().Single();
    Require(
        item.GetProperty("decision").GetString() == "rejected",
        "batch report decision was omitted or changed");
}

var cyclicPayload = new Dictionary<string, object?>();
cyclicPayload["self"] = cyclicPayload;
object deepPayload = "leaf";
for (var depth = 0; depth < 66; depth++) deepPayload = new object?[] { deepPayload };
var invalidPayloads = new object[]
{
    new Dictionary<string, object?>
    {
        ["nested"] = new object?[] { new Dictionary<string, object?> { ["unsafe"] = 9_007_199_254_740_992L } },
    },
    new Dictionary<string, object?> { ["nested"] = double.NaN },
    new Dictionary<string, object?> { ["nested"] = double.PositiveInfinity },
    cyclicPayload,
    new Dictionary<string, object?> { ["nested"] = deepPayload },
};
var requestsBeforeInvalidPayloads = handler.RequestCount;
foreach (var invalidPayload in invalidPayloads)
{
    try
    {
        await client.ReportActionAsync(invalidPayload);
        throw new InvalidOperationException("invalid JSON payload was accepted");
    }
    catch (RinProtocolException exception)
    {
        Require(exception.Code == "invalid_request", "invalid JSON payload returned the wrong error");
    }
}
Require(
    handler.RequestCount == requestsBeforeInvalidPayloads,
    "invalid JSON payload reached the transport");

var apiErrorHandler = new RecordingHandler
{
    ForcedStatus = HttpStatusCode.BadRequest,
    ResponseBodyFactory = _ =>
        "{\"ok\":false,\"error\":{\"code\":\"invalid_request\",\"message\":\"safe\",\"field\":\"actor_id\"}}",
};
using (var apiErrorClient = new RinClient(new RinClientOptions(), apiErrorHandler))
{
    try
    {
        await apiErrorClient.HealthAsync();
        throw new InvalidOperationException("API error envelope was accepted");
    }
    catch (RinApiException exception)
    {
        Require(exception.Status == 400, "API error status changed");
        Require(exception.Code == "invalid_request", "API error code changed");
        Require(exception.Field == "actor_id", "API error field changed");
    }
}

RequireThrows<RinConfigurationException>(() => new RinClient(new RinClientOptions
{
    BaseUrl = "http://models.example",
    Token = "fixture",
}), "remote HTTP origin was accepted");
RequireThrows<RinConfigurationException>(() => new RinClient(new RinClientOptions
{
    BaseUrl = "https://models.example",
}), "remote origin without token was accepted");
RequireThrows<RinConfigurationException>(
    () => client.GetProposalJobAsync("\u4f5c\u4e1a"),
    "Unicode path ID was accepted");

var oversized = new RecordingHandler { DeclaredLength = 2048 };
using var limited = new RinClient(new RinClientOptions { MaxResponseBytes = 1024 }, oversized);
try
{
    await limited.HealthAsync();
    throw new InvalidOperationException("oversized response was accepted");
}
catch (RinProtocolException exception)
{
    Require(exception.Code == "response_too_large", "wrong response limit error");
}

var slow = new RecordingHandler { ContentFactory = () => new StreamContent(new SlowStream()) };
using var impatient = new RinClient(new RinClientOptions { Timeout = TimeSpan.FromMilliseconds(50) }, slow);
try
{
    await impatient.HealthAsync();
    throw new InvalidOperationException("slow response exceeded the request deadline");
}
catch (RinTransportException exception)
{
    Require(exception.Code == "transport_timeout", "wrong timeout error");
}

var proposalRace = new RecordingHandler
{
    ResponseBodyFactory = request => request.Method == HttpMethod.Delete
        ? ProposalJobBody(
            "succeeded",
            ",\"proposal\":{\"id\":\"proposal.race\",\"session_id\":\"session.fixture\",\"request_id\":\"request.fixture\",\"actor_id\":\"actor.fixture\",\"tick\":7}")
        : ProposalJobBody("running"),
};
using var proposalRaceClient = new RinClient(new RinClientOptions(), proposalRace);
var proposalRaceJob = await proposalRaceClient.WaitForProposalAsync(
    "job.fixture",
    TimeSpan.FromMilliseconds(50),
    TimeSpan.FromMilliseconds(10));
Require(
    proposalRaceJob.GetProperty("proposal").GetProperty("id").GetString() == "proposal.race",
    "proposal completion returned by cancellation was discarded");

var generationRace = new RecordingHandler
{
    ResponseBodyFactory = request => request.Method == HttpMethod.Delete
        ? GenerationJobBody("succeeded", ",\"result\":{\"content\":\"finished at the deadline\"}")
        : GenerationJobBody("queued"),
};
using var generationRaceClient = new RinClient(new RinClientOptions(), generationRace);
var generationRaceJob = await generationRaceClient.WaitForGenerationAsync(
    "job.fixture",
    TimeSpan.FromMilliseconds(50),
    TimeSpan.FromMilliseconds(10));
Require(
    generationRaceJob.GetProperty("result").GetProperty("content").GetString() == "finished at the deadline",
    "generation completion returned by cancellation was discarded");

var terminalCancel = new RecordingHandler
{
    ResponseBodyFactory = request => request.Method == HttpMethod.Delete
        ? ProposalJobBody("stale", ",\"error\":{\"code\":\"proposal_stale\",\"message\":\"World changed\"}")
        : ProposalJobBody("running"),
};
using var terminalCancelClient = new RinClient(new RinClientOptions(), terminalCancel);
try
{
    await terminalCancelClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromMilliseconds(50),
        TimeSpan.FromMilliseconds(10));
    throw new InvalidOperationException("terminal cancellation result was discarded");
}
catch (RinApiException exception)
{
    Require(exception.Code == "proposal_stale", "terminal cancellation result became job_timeout");
}

var canceledDuringGet = new CancellationReconciliationHandler(
    ProposalJobBody(
        "succeeded",
        ",\"proposal\":{\"id\":\"proposal.after-cancel\",\"session_id\":\"session.fixture\",\"request_id\":\"request.fixture\",\"actor_id\":\"actor.fixture\",\"tick\":8}"),
    blockGetUntilCanceled: true);
using var canceledDuringGetClient = new RinClient(new RinClientOptions(), canceledDuringGet);
using (var callerCancellation = new CancellationTokenSource())
{
    var wait = canceledDuringGetClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromMilliseconds(10),
        callerCancellation.Token);
    await canceledDuringGet.GetStarted;
    callerCancellation.Cancel();
    var reconciled = await wait;
    Require(canceledDuringGet.DeleteCount == 1, "caller cancellation during GET did not issue DELETE");
    Require(
        reconciled.GetProperty("proposal").GetProperty("id").GetString() == "proposal.after-cancel",
        "proposal raced with caller cancellation was discarded");
}

var confirmedCallerCancellation = new CancellationReconciliationHandler(
    ProposalJobBody("canceled"));
using var confirmedCallerCancellationClient = new RinClient(new RinClientOptions(), confirmedCallerCancellation);
using (var callerCancellation = new CancellationTokenSource())
{
    var wait = confirmedCallerCancellationClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromSeconds(5),
        callerCancellation.Token);
    await confirmedCallerCancellation.GetStarted;
    callerCancellation.Cancel();
    try
    {
        await wait;
        throw new InvalidOperationException("confirmed caller cancellation did not remain canceled");
    }
    catch (OperationCanceledException)
    {
        Require(callerCancellation.IsCancellationRequested, "wrong cancellation was propagated");
    }
    Require(confirmedCallerCancellation.DeleteCount == 1, "caller cancellation during delay did not issue DELETE");
}

var unconfirmedCallerCancellation = new CancellationReconciliationHandler(
    ProposalJobBody("running"));
using var unconfirmedCallerCancellationClient = new RinClient(new RinClientOptions(), unconfirmedCallerCancellation);
using (var callerCancellation = new CancellationTokenSource())
{
    var wait = unconfirmedCallerCancellationClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromSeconds(5),
        callerCancellation.Token);
    await unconfirmedCallerCancellation.GetStarted;
    callerCancellation.Cancel();
    try
    {
        await wait;
        throw new InvalidOperationException("unconfirmed caller cancellation was treated as safe");
    }
    catch (RinApiException exception)
    {
        Require(exception.Code == "job_outcome_unknown", "unresolved DELETE returned the wrong error");
    }
}

var staleCallerReconciliation = new CancellationReconciliationHandler(
    ProposalJobBody("stale", ",\"error\":{\"code\":\"proposal_stale\",\"message\":\"World changed\"}"));
using var staleCallerReconciliationClient = new RinClient(new RinClientOptions(), staleCallerReconciliation);
using (var callerCancellation = new CancellationTokenSource())
{
    var wait = staleCallerReconciliationClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromSeconds(5),
        callerCancellation.Token);
    await staleCallerReconciliation.GetStarted;
    callerCancellation.Cancel();
    try
    {
        await wait;
        throw new InvalidOperationException("stale cancellation reconciliation was discarded");
    }
    catch (RinApiException exception)
    {
        Require(exception.Code == "proposal_stale", "stale DELETE terminal result was not propagated");
    }
}

var failedCallerReconciliation = new CancellationReconciliationHandler(
    "{\"ok\":false,\"error\":{\"code\":\"cancel_failed\",\"message\":\"Cancellation failed\"}}");
using var failedCallerReconciliationClient = new RinClient(new RinClientOptions(), failedCallerReconciliation);
using (var callerCancellation = new CancellationTokenSource())
{
    var wait = failedCallerReconciliationClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromSeconds(5),
        callerCancellation.Token);
    await failedCallerReconciliation.GetStarted;
    callerCancellation.Cancel();
    try
    {
        await wait;
        throw new InvalidOperationException("failed cancellation reconciliation was treated as safe");
    }
    catch (RinApiException exception)
    {
        Require(exception.Code == "job_cancel_unconfirmed", "failed DELETE returned the wrong error");
    }
}

var malformedCallerReconciliation = new CancellationReconciliationHandler("not-json");
using var malformedCallerReconciliationClient = new RinClient(new RinClientOptions(), malformedCallerReconciliation);
using (var callerCancellation = new CancellationTokenSource())
{
    var wait = malformedCallerReconciliationClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromSeconds(5),
        callerCancellation.Token);
    await malformedCallerReconciliation.GetStarted;
    callerCancellation.Cancel();
    try
    {
        await wait;
        throw new InvalidOperationException("malformed cancellation reconciliation was treated as safe");
    }
    catch (RinApiException exception)
    {
        Require(exception.Code == "job_cancel_unconfirmed", "malformed DELETE returned the wrong error");
    }
}

var crossedGet = new RecordingHandler
{
    ResponseBodyFactory = _ => ProposalJobBody("running", jobId: "job.other"),
};
using var crossedGetClient = new RinClient(new RinClientOptions(), crossedGet);
try
{
    await crossedGetClient.WaitForProposalAsync("job.fixture");
    throw new InvalidOperationException("crossed GET job identity was accepted");
}
catch (RinProtocolException exception)
{
    Require(exception.Code == "invalid_job", "crossed GET returned the wrong error");
}

foreach (var malformedStatus in new[] { "", " canceled ", "canceled\\u0000" })
{
    var malformedStatusGet = new RecordingHandler
    {
        ResponseBodyFactory = _ => ProposalJobBody(malformedStatus),
    };
    using var malformedStatusGetClient = new RinClient(new RinClientOptions(), malformedStatusGet);
    try
    {
        await malformedStatusGetClient.WaitForProposalAsync("job.fixture");
        throw new InvalidOperationException("polling accepted a normalized pseudo-status");
    }
    catch (RinProtocolException exception)
    {
        Require(exception.Code == "invalid_job", "malformed polling status returned the wrong error");
    }
}

foreach (var malformedStatus in new[] { "", " canceled ", "canceled\\u0000" })
{
    var malformedStatusCancellation = new CancellationReconciliationHandler(
        ProposalJobBody(malformedStatus));
    using var malformedStatusCancellationClient =
        new RinClient(new RinClientOptions(), malformedStatusCancellation);
    using var callerCancellation = new CancellationTokenSource();
    var wait = malformedStatusCancellationClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromSeconds(5),
        TimeSpan.FromSeconds(5),
        callerCancellation.Token);
    await malformedStatusCancellation.GetStarted;
    callerCancellation.Cancel();
    try
    {
        await wait;
        throw new InvalidOperationException("caller cancellation accepted a normalized pseudo-status");
    }
    catch (RinApiException exception)
    {
        Require(
            exception.Code == "job_outcome_unknown",
            "malformed cancellation status returned the wrong error");
    }
}

var malformedDelete = new RecordingHandler
{
    ResponseBodyFactory = request => request.Method == HttpMethod.Delete
        ? ProposalJobBody(
            "succeeded",
            ",\"proposal\":{\"id\":\"proposal.race\",\"session_id\":\"session.fixture\",\"request_id\":\"request.fixture\",\"actor_id\":\"actor.fixture\",\"tick\":9007199254740992}")
        : ProposalJobBody("running"),
};
using var malformedDeleteClient = new RinClient(new RinClientOptions(), malformedDelete);
try
{
    await malformedDeleteClient.WaitForProposalAsync(
        "job.fixture",
        TimeSpan.FromMilliseconds(50),
        TimeSpan.FromMilliseconds(10));
    throw new InvalidOperationException("malformed DELETE proposal identity was accepted");
}
catch (RinProtocolException exception)
{
    Require(exception.Code == "invalid_job", "malformed DELETE returned the wrong error");
}

Console.WriteLine("Rin C# SDK tests passed");

static void Require(bool condition, string message)
{
    if (!condition) throw new InvalidOperationException(message);
}

static void RequireThrows<TException>(Action action, string message) where TException : Exception
{
    try
    {
        action();
        throw new InvalidOperationException(message);
    }
    catch (TException)
    {
    }
}

static string RouteKey(string name, string method, string path, int status) =>
    name + " " + method + " " + path + " " + status;

static string[] ContractRouteKeys()
{
    using var document = JsonDocument.Parse(File.ReadAllText(ContractManifestPath()));
    return document.RootElement.GetProperty("operations")
        .EnumerateArray()
        .Where(operation =>
            operation.GetProperty("profile").GetString() != "operational")
        .Select(operation => RouteKey(
            operation.GetProperty("name").GetString() ?? string.Empty,
            operation.GetProperty("method").GetString() ?? string.Empty,
            operation.GetProperty("path").GetString() ?? string.Empty,
            operation.GetProperty("status").GetInt32()))
        .OrderBy(route => route, StringComparer.Ordinal)
        .ToArray();
}

static string ContractManifestPath()
{
    foreach (var start in new[] { Directory.GetCurrentDirectory(), AppContext.BaseDirectory })
    {
        for (DirectoryInfo? directory = new(start); directory is not null; directory = directory.Parent)
        {
            foreach (var relative in new[]
            {
                Path.Combine("sdk", "conformance", "routes.json"),
                Path.Combine("conformance", "routes.json"),
            })
            {
                var candidate = Path.Combine(directory.FullName, relative);
                if (File.Exists(candidate)) return candidate;
            }
        }
    }
    throw new FileNotFoundException("cannot locate sdk/conformance/routes.json");
}

static Epoch TestEpoch(string sessionId) =>
    new(sessionId, "world.fixture", 1, 1, 1);

static DecisionWindow TestWindow(string sessionId, string actorId, long tick)
{
    var epoch = TestEpoch(sessionId);
    return new DecisionWindow(
        "window.fixture",
        "sequential",
        epoch,
        (ulong)tick,
        new Timepoint("event", tick),
        new Timepoint("event", tick + 1),
        new[] { actorId });
}

static ActionOfferInput TestOffer(string sessionId, string actorId, long tick)
{
    var window = TestWindow(sessionId, actorId, tick);
    return new ActionOfferInput(
        "offer.talk",
        window.Id,
        actorId,
        new CapabilityRef("dialogue.talk", "1"),
        new string('a', 64),
        "Talk",
        JsonSerializer.SerializeToElement(new Dictionary<string, object?>()),
        window.Epoch,
        window.ObservationSeq,
        window.Deadline);
}

static ProposeRequest TestProposeRequest(
    string sessionId,
    string requestId,
    string actorId,
    long tick)
{
    var window = TestWindow(sessionId, actorId, tick);
    return new ProposeRequest(
        sessionId,
        requestId,
        actorId,
        "Talk",
        window,
        new[] { TestOffer(sessionId, actorId, tick) })
    {
        Tick = tick,
    };
}

static ReportActionRequest RejectedReport(
    string sessionId,
    string requestId,
    string proposalId,
    string eventId) =>
    new(
        sessionId,
        requestId,
        5,
        new ActionReportInput(
            proposalId,
            eventId,
            "rejected",
            "host rejected the offer"));

static Dictionary<string, object?> TestProposal(
    string proposalId,
    string sessionId,
    string requestId,
    string actorId,
    long tick)
{
    return new Dictionary<string, object?>
    {
        ["id"] = proposalId,
        ["session_id"] = sessionId,
        ["request_id"] = requestId,
        ["actor_id"] = actorId,
        ["tick"] = tick,
        ["based_on_revision"] = 1,
        ["based_on_head_hash"] = string.Empty,
        ["created_revision"] = 2,
        ["decision_window"] = TestWindow(sessionId, actorId, tick),
        ["action"] = TestOffer(sessionId, actorId, tick),
        ["stance"] = "engage",
        ["summary"] = "Talk",
        ["rationale"] = "Useful",
        ["status"] = "pending",
    };
}

static string ProposalJobBody(
    string status,
    string suffix = "",
    string jobId = "job.fixture",
    string sessionId = "session.fixture",
    string requestId = "request.fixture") =>
    "{\"ok\":true,\"data\":{\"job_id\":\"" + jobId +
    "\",\"session_id\":\"" + sessionId +
    "\",\"request_id\":\"" + requestId +
    "\",\"status\":\"" + status + "\"" + suffix + "}}";

static string GenerationJobBody(
    string status,
    string suffix = "",
    string jobId = "job.fixture",
    string requestId = "generation.fixture") =>
    "{\"ok\":true,\"data\":{\"job_id\":\"" + jobId +
    "\",\"request_id\":\"" + requestId +
    "\",\"status\":\"" + status + "\"" + suffix + "}}";

sealed class RecordingHandler : HttpMessageHandler
{
    public int RequestCount { get; private set; }
    public HttpMethod? Method { get; private set; }
    public string Path { get; private set; } = string.Empty;
    public string Authorization { get; private set; } = string.Empty;
    public string UserAgent { get; private set; } = string.Empty;
    public string ExpectedGameId { get; private set; } = string.Empty;
    public string ContentType { get; private set; } = string.Empty;
    public string Body { get; private set; } = string.Empty;
    public HttpStatusCode Status { get; private set; }
    public long? DeclaredLength { get; init; }
    public HttpStatusCode? ForcedStatus { get; init; }
    public Func<HttpContent>? ContentFactory { get; init; }
    public Func<HttpRequestMessage, string>? ResponseBodyFactory { get; init; }
    public string? TransferResponseBody { get; init; }

    protected override async Task<HttpResponseMessage> SendAsync(
        HttpRequestMessage request,
        CancellationToken cancellationToken)
    {
        cancellationToken.ThrowIfCancellationRequested();
        RequestCount++;
        Method = request.Method;
        Path = request.RequestUri?.AbsolutePath ?? string.Empty;
        Authorization = request.Headers.TryGetValues("Authorization", out var values)
            ? values.Single()
            : string.Empty;
        UserAgent = request.Headers.UserAgent.ToString();
        ExpectedGameId = request.Headers.TryGetValues(
            "Rin-Expected-Game-Id",
            out var gameIds)
                ? gameIds.Single()
                : string.Empty;
        ContentType = request.Content?.Headers.ContentType?.MediaType ??
            string.Empty;
        Body = request.Content is null
            ? string.Empty
            : await request.Content.ReadAsStringAsync(cancellationToken);
        var status = ForcedStatus ?? (Path is "/v2/jobs/propose" or "/v2/generation/jobs"
            ? HttpStatusCode.Accepted
            : HttpStatusCode.OK);
        Status = status;
        var responseBodyFactory = ResponseBodyFactory;
        var content = Path == "/v2/session/export"
            ? new ByteArrayContent(Encoding.UTF8.GetBytes(
                TransferResponseBody ?? TransferFixture.Body()))
            : responseBodyFactory is not null
            ? new ByteArrayContent(Encoding.UTF8.GetBytes(responseBodyFactory(request)))
            : ContentFactory?.Invoke() ??
              new ByteArrayContent(Encoding.UTF8.GetBytes("{\"ok\":true,\"data\":{\"status\":\"ok\"}}"));
        if (Path == "/v2/session/export")
        {
            content.Headers.ContentType =
                new System.Net.Http.Headers.MediaTypeHeaderValue(
                    "application/x-ndjson");
        }
        if (DeclaredLength.HasValue) content.Headers.ContentLength = DeclaredLength.Value;
        return new HttpResponseMessage(status) { Content = content };
    }
}

sealed class TestAuthoritativeStore : IWorkflowStore
{
    public ProposalAttempt? Attempt { get; private set; }

    public List<OutcomeOutboxEntry> Outcomes { get; } = new();

    public ValueTask<ProposalAttempt?> LoadAsync(
        CancellationToken cancellationToken = default) =>
        ValueTask.FromResult(Attempt);

    public ValueTask SaveAsync(
        ProposalAttempt attempt,
        CancellationToken cancellationToken = default)
    {
        Attempt = attempt;
        return ValueTask.CompletedTask;
    }

    public ValueTask<bool> CreateAsync(
        ProposalAttempt attempt,
        CancellationToken cancellationToken = default)
    {
        if (Attempt is not null)
        {
            return ValueTask.FromResult(false);
        }
        Attempt = attempt;
        return ValueTask.FromResult(true);
    }

    public async ValueTask SettleAsync(
        ProposalAttempt attempt,
        ActionProposal proposal,
        ReportActionRequest commit,
        Func<CancellationToken, ValueTask> apply,
        CancellationToken cancellationToken = default)
    {
        if (Attempt != attempt)
        {
            throw new InvalidOperationException(
                "settlement did not use the stored Attempt");
        }
        await apply(cancellationToken);
        Outcomes.Add(new OutcomeOutboxEntry("outcome.workflow", commit));
        Attempt = null;
    }

    public ValueTask CompleteAsync(
        ProposalAttempt attempt,
        ActionProposal proposal,
        ReportActionRequest commit,
        CancellationToken cancellationToken = default)
    {
        if (Attempt != attempt)
        {
            throw new InvalidOperationException(
                "completion did not use the stored Attempt");
        }
        Outcomes.Add(new OutcomeOutboxEntry("outcome.workflow", commit));
        Attempt = null;
        return ValueTask.CompletedTask;
    }

    public ValueTask<IReadOnlyList<OutcomeOutboxEntry>> ListAsync(
        CancellationToken cancellationToken = default) =>
        ValueTask.FromResult<IReadOnlyList<OutcomeOutboxEntry>>(Outcomes.ToArray());

    public ValueTask AcknowledgeAsync(
        OutcomeOutboxEntry entry,
        MutationResult result,
        CancellationToken cancellationToken = default)
    {
        if (!result.Duplicate)
        {
            throw new InvalidOperationException(
                "test expected an exact duplicate acknowledgement");
        }
        Outcomes.Remove(entry);
        return ValueTask.CompletedTask;
    }

}

sealed class TestOpaqueSnapshotStore : IOpaqueSnapshotStore
{
    private byte[] value = Array.Empty<byte>();

    public ValueTask PutAsync(
        string key,
        byte[] snapshot,
        CancellationToken cancellationToken = default)
    {
        value = snapshot.ToArray();
        return ValueTask.CompletedTask;
    }

    public ValueTask<byte[]> GetAsync(
        string key,
        CancellationToken cancellationToken = default) =>
        ValueTask.FromResult(value.ToArray());
}

static class TransferFixture
{
    internal static string Body()
    {
        var head = new string('a', 64);
        var digest = new string('b', 64);
        return JsonSerializer.Serialize(new
        {
            type = "manifest",
            transfer_version = "rin.session-transfer/v1",
            session_id = "session.fixture",
            terminal_revision = 1,
            terminal_head_hash = head,
            event_count = 1,
        }) + "\n" + JsonSerializer.Serialize(new
        {
            type = "event",
            record = new { sequence = 1 },
            record_sha256 = digest,
        }) + "\n" + JsonSerializer.Serialize(new
        {
            type = "complete",
            terminal_revision = 1,
            terminal_head_hash = head,
            event_count = 1,
            stream_sha256 = digest,
        }) + "\n";
    }
}

sealed class SlowStream : Stream
{
    public override bool CanRead => true;
    public override bool CanSeek => false;
    public override bool CanWrite => false;
    public override long Length => throw new NotSupportedException();
    public override long Position
    {
        get => throw new NotSupportedException();
        set => throw new NotSupportedException();
    }

    public override void Flush() { }
    public override int Read(byte[] buffer, int offset, int count) => throw new NotSupportedException();
    public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
    public override void SetLength(long value) => throw new NotSupportedException();
    public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();

    public override async ValueTask<int> ReadAsync(
        Memory<byte> buffer,
        CancellationToken cancellationToken = default)
    {
        await Task.Delay(Timeout.InfiniteTimeSpan, cancellationToken);
        return 0;
    }
}

sealed class CancellationReconciliationHandler : HttpMessageHandler
{
    private readonly string deleteResponseBody;
    private readonly bool blockGetUntilCanceled;
    private readonly TaskCompletionSource<bool> getStarted =
        new(TaskCreationOptions.RunContinuationsAsynchronously);

    public CancellationReconciliationHandler(string deleteResponseBody, bool blockGetUntilCanceled = false)
    {
        this.deleteResponseBody = deleteResponseBody;
        this.blockGetUntilCanceled = blockGetUntilCanceled;
    }

    public Task GetStarted => getStarted.Task;

    public int DeleteCount { get; private set; }

    protected override async Task<HttpResponseMessage> SendAsync(
        HttpRequestMessage request,
        CancellationToken cancellationToken)
    {
        if (request.Method == HttpMethod.Delete)
        {
            if (cancellationToken.IsCancellationRequested)
            {
                throw new InvalidOperationException("DELETE reused the canceled caller token");
            }
            DeleteCount++;
            return Response(deleteResponseBody);
        }

        getStarted.TrySetResult(true);
        if (blockGetUntilCanceled)
        {
            await Task.Delay(Timeout.InfiniteTimeSpan, cancellationToken);
        }
        return Response(
            "{\"ok\":true,\"data\":{\"job_id\":\"job.fixture\",\"session_id\":\"session.fixture\"," +
            "\"request_id\":\"request.fixture\",\"status\":\"running\"}}");
    }

    private static HttpResponseMessage Response(string body) =>
        new(HttpStatusCode.OK)
        {
            Content = new ByteArrayContent(Encoding.UTF8.GetBytes(body)),
        };
}
