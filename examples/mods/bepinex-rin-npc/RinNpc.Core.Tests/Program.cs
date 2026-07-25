using Rin.Client;
using RinNpcExample;

var directory = Path.Combine(
    Path.GetTempPath(),
    "rin-bepinex-state-tests-" + Guid.NewGuid().ToString("N"));
Directory.CreateDirectory(directory);
try
{
    const string product = "example.game";
    const string save = "profile-1";
    var state = BepInExWorkflowState.Open(directory, product, save);
    var stateFile = Directory.GetFiles(directory, "*.json").Single();
    var sessionId = state.SessionId;
    var create = new CreateSessionRequest(
        "create." + sessionId,
        sessionId,
        new RinBinding(
            "unity-bepinex",
            "test",
            "1",
            "sha256:" + new string('0', 64)),
        Array.Empty<ActorSeedInput>());
    var propose = new ProposeRequest(
        sessionId,
        "propose." + sessionId,
        "npc.test",
        "test",
        new[] { new ActionSpecInput("wait", "wait", "wait") });
    var observe = new
    {
        session_id = sessionId,
        request_id = "observe." + sessionId,
        event_id = "event." + sessionId,
    };

    state.StageTurnContext(create, observe);
    var attempt = ProposalAttempt.Create(sessionId + ".1", propose);
    Require(await state.CreateAsync(attempt), "Staged Pending Turn was not created");
    await state.SaveAsync(attempt with { JobId = "job.stable" });

    var restarted = BepInExWorkflowState.Open(directory, product, save);
    Require(restarted.SessionId == sessionId, "Session identity changed after restart");
    Require(restarted.Sequence == 1, "Turn sequence did not survive restart");
    Require(restarted.CreateRequest?.SessionId == sessionId, "Create request was lost");
    Require(restarted.PendingObserve is not null, "Observe request was lost");
    var pending = await restarted.LoadAsync();
    Require(pending?.JobId == "job.stable", "Pending Job identity was lost");
    Require(
        restarted.ApplyQuestEffect(pending!.OperationId, "offer_quest"),
        "Quest transition was not persisted");
    var afterQuestRestart = BepInExWorkflowState.Open(directory, product, save);
    Require(afterQuestRestart.QuestStage == 1, "Quest stage did not survive restart");
    Require(
        afterQuestRestart.ApplyQuestEffect(pending.OperationId, "offer_quest"),
        "Idempotent quest replay failed");
    Require(afterQuestRestart.QuestStage == 1, "Quest replay duplicated its effect");
    restarted = afterQuestRestart;

    var proposal = new ActionProposal(
        "proposal.test",
        sessionId,
        propose.RequestId,
        "npc.test",
        2,
        1,
        "sha256:" + new string('1', 64),
        2,
        new ActionSpecInput("wait", "wait", "wait"),
        "neutral",
        "wait",
        "test",
        "ready");
    var commit = new CommitRequest(
        sessionId,
        "commit." + sessionId,
        proposal.Id,
        "outcome." + sessionId,
        true);
    var fallback = new
    {
        session_id = sessionId,
        request_id = "fallback." + sessionId,
        event_id = commit.EventId,
    };
    await restarted.CompleteWithFallbackAsync(
        pending,
        proposal,
        commit,
        fallback);

    var withOutcome = BepInExWorkflowState.Open(directory, product, save);
    Require(await withOutcome.LoadAsync() is null, "Completed Pending Turn survived");
    var outcomes = await withOutcome.ListAsync();
    Require(outcomes.Count == 1, "Outcome did not survive restart");
    Require(outcomes[0].Commit.RequestId == commit.RequestId, "Commit changed");
    Require(outcomes[0].FallbackObserve is not null, "Fallback Observe was lost");

    var converted = await withOutcome.ReplaceWithFallbackAsync(outcomes[0]);
    var afterConversion = BepInExWorkflowState.Open(directory, product, save);
    var degraded = (await afterConversion.ListAsync()).Single();
    Require(degraded.IsDegradedObserve, "Fallback conversion was not durable");
    await afterConversion.AcknowledgeAsync(
        degraded,
        new MutationResult(sessionId, 3, "sha256:" + new string('2', 64), false));
    Require((await afterConversion.ListAsync()).Count == 0, "ACK did not remove Outcome");

    afterConversion.StageTurnContext(create, observe);
    var secondAttempt = ProposalAttempt.Create(
        sessionId + ".2",
        propose with { RequestId = "propose.second" });
    Directory.CreateDirectory(stateFile + ".next");
    try
    {
        await afterConversion.CreateAsync(secondAttempt);
        throw new InvalidOperationException("Blocked state replacement was accepted");
    }
    catch (Exception exception)
        when (exception is IOException or UnauthorizedAccessException)
    {
        // Expected: failed persistence must not publish the candidate in memory.
    }
    Require(afterConversion.Sequence == 1, "Failed persistence advanced in-memory sequence");
    Require(await afterConversion.LoadAsync() is null, "Failed persistence published Pending Turn");
    Directory.Delete(stateFile + ".next");

    var same = BepInExWorkflowState.Open(directory, product, save);
    Require(same.SessionId == sessionId, "Stable identity was not reused");
    File.WriteAllText(
        Directory.GetFiles(directory, "*.json")
            .Single(path => File.ReadAllText(path).Contains("\"saveIdentity\":\"profile-1\"")) +
        ".next",
        "interrupted replacement");
    Require(
        BepInExWorkflowState.Open(directory, product, save).SessionId == sessionId,
        "An interrupted temporary replacement displaced valid state");
    var other = BepInExWorkflowState.Open(directory, product, "profile-2");
    Require(other.SessionId != sessionId, "Different save identities collided");
    Require(
        Directory.GetFiles(directory).All(path =>
            Path.GetFileName(path).All(IsSafeFilenameCharacter)),
        "State filename is not Windows-safe");

    File.WriteAllText(stateFile, "{\"version\":999}");
    ExpectFailure(
        () => BepInExWorkflowState.Open(directory, product, save),
        "Unsupported state version was accepted");
    File.WriteAllBytes(stateFile, new byte[2_000_001]);
    ExpectFailure(
        () => BepInExWorkflowState.Open(directory, product, save),
        "Oversized state was accepted");
    ExpectConfigurationFailure(
        () => BepInExWorkflowState.Open(directory, product, "bad\nidentity"),
        "Unsafe save identity was accepted");

    Console.WriteLine("BepInEx workflow restart tests passed.");
}
finally
{
    Directory.Delete(directory, recursive: true);
}

static void Require(bool condition, string message)
{
    if (!condition) throw new InvalidOperationException(message);
}

static void ExpectFailure(Action action, string message)
{
    try
    {
        action();
    }
    catch (InvalidDataException)
    {
        return;
    }
    throw new InvalidOperationException(message);
}

static void ExpectConfigurationFailure(Action action, string message)
{
    try
    {
        action();
    }
    catch (RinConfigurationException)
    {
        return;
    }
    throw new InvalidOperationException(message);
}

static bool IsSafeFilenameCharacter(char character) =>
    character is >= 'a' and <= 'z' or
        >= 'A' and <= 'Z' or
        >= '0' and <= '9' or
        '-' or '.';
