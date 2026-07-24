using System;
using System.Collections;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using UnityEngine;
using UnityEngine.Networking;

// Unity 2021+ adapter for Rin Protocol v1. It uses coroutines so model work
// never blocks the render thread and has no package dependencies.
public sealed class RinClient : MonoBehaviour
{
    public const string ProtocolVersion = "rin.protocol/v1";
    private static readonly string[] AmbiguousProposalErrors =
    {
        "proposal_outcome_unknown",
        "job_outcome_unknown",
        "job_cancel_unconfirmed",
        "job_timeout",
        "job_id_persistence_failed",
    };

    [SerializeField] private string baseUrl = "http://127.0.0.1:7374";
    [SerializeField] private string token = "";
    [SerializeField, Range(1, 120)] private int requestTimeoutSeconds = 5;
    [SerializeField, Range(1, 300)] private int jobDeadlineSeconds = 25;
    [SerializeField, Range(0.05f, 5f)] private float pollIntervalSeconds = 0.1f;
    [SerializeField] private int maxResponseBytes = 32 * 1024 * 1024;

    public bool IsConfigured { get; private set; }

    private void Awake()
    {
        baseUrl = (baseUrl ?? "").Trim().TrimEnd('/');
        IsConfigured = ValidateEndpoint(baseUrl, token);
        if (!IsConfigured)
        {
            Debug.LogWarning("Rin adapter disabled because its endpoint is invalid.");
        }
    }

    public IEnumerator Observe(ObserveRequest request, Action<MutationResult> completed)
    {
        var call = new CallResult();
        yield return Send("POST", "/v1/session/observe", JsonUtility.ToJson(request), 200, call);
        completed(call.Ok ? JsonUtility.FromJson<MutationEnvelope>(call.Text).data : null);
    }

    public IEnumerator CreateSession(CreateSessionRequest request, Action<MutationResult> completed)
    {
        var call = new CallResult();
        yield return Send("POST", "/v1/session/create", JsonUtility.ToJson(request), 200, call);
        completed(call.Ok ? JsonUtility.FromJson<MutationEnvelope>(call.Text).data : null);
    }

    public IEnumerator ProposalFreshness(
        SessionRequest request,
        string proposalId,
        Action<ProposalFreshnessResult> completed)
    {
        var call = new CallResult();
        yield return Send("POST", "/v1/session/get", JsonUtility.ToJson(request), 200, call);
        if (!call.Ok)
        {
            completed(null);
            yield break;
        }
        try
        {
            var envelope = JsonUtility.FromJson<StateEnvelope>(call.Text);
            if (envelope == null || envelope.data == null)
            {
                completed(null);
                yield break;
            }
            string proposalJson;
            ActionProposal proposal = null;
            if (TryExtractObjectProperty(
                call.Text,
                "proposals",
                proposalId,
                out proposalJson))
            {
                proposal = JsonUtility.FromJson<ActionProposal>(proposalJson);
                if (proposal != null)
                {
                    proposal.has_unsupported_action_parameters =
                        ActionHasUnsupportedParameters(proposalJson);
                }
            }
            completed(new ProposalFreshnessResult
            {
                revision = envelope.data.revision,
                world_revision = envelope.data.world_revision,
                proposal = proposal,
            });
        }
        catch (ArgumentException)
        {
            completed(null);
        }
    }

    public IEnumerator Commit(CommitRequest request, Action<MutationResult> completed)
    {
        var call = new CallResult();
        yield return Send("POST", "/v1/action/commit", JsonUtility.ToJson(request), 200, call);
        completed(call.Ok ? JsonUtility.FromJson<MutationEnvelope>(call.Text).data : null);
    }

    public IEnumerator CommitReport(CommitRequest request, Action<ReportAttempt> completed)
    {
        var call = new CallResult();
        yield return Send("POST", "/v1/action/commit", JsonUtility.ToJson(request), 200, call);
        completed(new ReportAttempt
        {
            ok = call.Ok,
            error_code = call.Ok ? "" : call.ErrorCode,
            data = call.Ok ? JsonUtility.FromJson<MutationEnvelope>(call.Text).data : null,
        });
    }

    public IEnumerator CommitBatch(BatchCommitRequest request, Action<MutationResult> completed)
    {
        var call = new CallResult();
        yield return Send("POST", "/v1/action/commit-batch", JsonUtility.ToJson(request), 200, call);
        completed(call.Ok ? JsonUtility.FromJson<MutationEnvelope>(call.Text).data : null);
    }

    public IEnumerator SetActorActivity(SetActorActivityRequest request, Action<MutationResult> completed)
    {
        var call = new CallResult();
        yield return Send("POST", "/v1/session/activity", JsonUtility.ToJson(request), 200, call);
        completed(call.Ok ? JsonUtility.FromJson<MutationEnvelope>(call.Text).data : null);
    }

    public IEnumerator DueAgents(DueAgentsRequest request, Action<DueAgentsResponse> completed)
    {
        var call = new CallResult();
        yield return Send("POST", "/v1/scheduler/due", JsonUtility.ToJson(request), 200, call);
        completed(call.Ok ? JsonUtility.FromJson<DueAgentsEnvelope>(call.Text).data : null);
    }

    public IEnumerator Arbitrate(ArbitrateRequest request, Action<ArbitrationResult> completed)
    {
        var call = new CallResult();
        yield return Send("POST", "/v1/world/arbitrate", JsonUtility.ToJson(request), 200, call);
        completed(call.Ok ? JsonUtility.FromJson<ArbitrationEnvelope>(call.Text).data : null);
    }

    public IEnumerator Timeline(TimelineRequest request, Action<TimelineResponse> completed)
    {
        var call = new CallResult();
        yield return Send("POST", "/v1/session/timeline", JsonUtility.ToJson(request), 200, call);
        completed(call.Ok ? JsonUtility.FromJson<TimelineEnvelope>(call.Text).data : null);
    }

    // JsonUtility cannot represent Rin's actor-id keyed state maps. Replay
    // therefore exposes the verified snapshot header. A dictionary-capable
    // JSON package can consume the same endpoint when full state is needed.
    public IEnumerator Replay(ReplayRequest request, Action<ReplaySnapshot> completed)
    {
        var call = new CallResult();
        yield return Send("POST", "/v1/session/replay", JsonUtility.ToJson(request), 200, call);
        completed(call.Ok ? JsonUtility.FromJson<ReplayEnvelope>(call.Text).data : null);
    }

    public IEnumerator ProposeWithFallback(
        ProposeRequest request,
        string fallbackActionId,
        Action<AdapterResult> completed,
        Func<bool> isCanceled = null,
        bool allowOfflineBeforeSubmit = true,
        string knownJobId = "",
        Func<string, bool> persistJobId = null)
    {
        var jobId = knownJobId;
        if (jobId == null || (jobId.Length > 0 && !IsValidProtocolId(jobId)))
        {
            completed(BuildClosedResult("invalid_job", ""));
            yield break;
        }
        if (!IsConfigured)
        {
            completed(
                allowOfflineBeforeSubmit && string.IsNullOrEmpty(jobId)
                    ? BuildOfflineResult(
                        request,
                        fallbackActionId,
                        "invalid_endpoint",
                        "")
                    : BuildClosedResult("proposal_outcome_unknown", jobId));
            yield break;
        }

        var recoveryPostUsed = false;
        if (string.IsNullOrEmpty(jobId))
        {
            var submitted = new SubmissionAttempt();
            yield return SubmitProposal(request, persistJobId, submitted);
            if (!submitted.ok)
            {
                // Send may have reached Rin even when its response was lost.
                // The persisted stable request remains resumable, so never run
                // a second, offline action after online submission begins.
                completed(BuildClosedResult(
                    "proposal_outcome_unknown",
                    submitted.job_id));
                yield break;
            }
            jobId = submitted.job_id;
        }

        var deadline = Time.realtimeSinceStartup + jobDeadlineSeconds;
        while (Time.realtimeSinceStartup < deadline)
        {
            if (!IsValidProtocolId(jobId))
            {
                completed(BuildClosedResult("invalid_job", ""));
                yield break;
            }
            if (isCanceled != null && isCanceled())
            {
                var cancelCall = new CallResult();
                yield return Send("DELETE", "/v1/jobs/" + UnityWebRequest.EscapeURL(jobId), null, 200, cancelCall);
                completed(ResolveCancellation(
                    cancelCall,
                    request,
                    fallbackActionId,
                    jobId,
                    false,
                    "job_cancel_unconfirmed"));
                yield break;
            }

            var pollCall = new CallResult();
            yield return Send("GET", "/v1/jobs/" + UnityWebRequest.EscapeURL(jobId), null, 200, pollCall);
            if (!pollCall.Ok)
            {
                if (pollCall.ErrorCode == "job_not_found" && !recoveryPostUsed)
                {
                    var recovered = new SubmissionAttempt();
                    recoveryPostUsed = true;
                    yield return SubmitProposal(request, persistJobId, recovered);
                    if (!recovered.ok)
                    {
                        completed(BuildClosedResult(
                            "proposal_outcome_unknown",
                            string.IsNullOrEmpty(recovered.job_id)
                                ? jobId
                                : recovered.job_id));
                        yield break;
                    }
                    jobId = recovered.job_id;
                    continue;
                }
                completed(BuildClosedResult("job_outcome_unknown", jobId));
                yield break;
            }
            JobEnvelope envelope;
            try
            {
                envelope = JsonUtility.FromJson<JobEnvelope>(pollCall.Text);
            }
            catch (ArgumentException)
            {
                completed(BuildClosedResult("invalid_job", jobId));
                yield break;
            }
            var job = envelope != null ? envelope.data : null;
            if (job == null)
            {
                completed(BuildClosedResult("invalid_job", jobId));
                yield break;
            }
            if (!JobMatchesRequest(job, jobId, request))
            {
                completed(BuildClosedResult("invalid_job_identity", jobId));
                yield break;
            }
            if (!JobShapeMatchesStatus(job, pollCall.Text))
            {
                completed(BuildClosedResult("invalid_job", jobId));
                yield break;
            }
            if (job.status == "succeeded")
            {
                if (job.proposal == null)
                {
                    completed(BuildClosedResult("invalid_job", jobId));
                    yield break;
                }
                if (!ProposalMatchesRequest(job.proposal, request, pollCall.Text))
                {
                    completed(BuildClosedResult("invalid_job_identity", jobId));
                    yield break;
                }
                completed(new AdapterResult
                {
                    source = "sidecar",
                    committable = true,
                    fallback_reason = "",
                    job_id = jobId,
                    proposal = job.proposal,
                });
                yield break;
            }
            if (job.status == "failed" || job.status == "stale" || job.status == "canceled")
            {
                string reason;
                if (!TryGetTerminalErrorCode(job, pollCall.Text, out reason))
                {
                    completed(BuildClosedResult("job_outcome_unknown", jobId));
                    yield break;
                }
                if (reason == "proposal_outcome_unknown" && !recoveryPostUsed)
                {
                    var recovered = new SubmissionAttempt();
                    recoveryPostUsed = true;
                    yield return SubmitProposal(request, persistJobId, recovered);
                    if (!recovered.ok)
                    {
                        completed(BuildClosedResult(
                            "proposal_outcome_unknown",
                            string.IsNullOrEmpty(recovered.job_id)
                                ? jobId
                                : recovered.job_id));
                        yield break;
                    }
                    jobId = recovered.job_id;
                    continue;
                }
                completed(BuildTerminalResult(
                    request,
                    fallbackActionId,
                    jobId,
                    reason,
                    true));
                yield break;
            }
            if (job.status != "queued" && job.status != "running")
            {
                completed(BuildClosedResult("invalid_job", jobId));
                yield break;
            }
            yield return new WaitForSecondsRealtime(pollIntervalSeconds);
        }

        var timeoutCancel = new CallResult();
        yield return Send("DELETE", "/v1/jobs/" + UnityWebRequest.EscapeURL(jobId), null, 200, timeoutCancel);
        completed(ResolveCancellation(
            timeoutCancel,
            request,
            fallbackActionId,
            jobId,
            true,
            "job_outcome_unknown"));
    }

    private IEnumerator SubmitProposal(
        ProposeRequest request,
        Func<string, bool> persistJobId,
        SubmissionAttempt result)
    {
        result.ok = false;
        var call = new CallResult();
        yield return Send(
            "POST",
            "/v1/jobs/propose",
            JsonUtility.ToJson(request),
            202,
            call);
        if (!call.Ok)
        {
            result.error_code = call.ErrorCode;
            yield break;
        }

        SubmissionEnvelope submission;
        try
        {
            submission = JsonUtility.FromJson<SubmissionEnvelope>(call.Text);
        }
        catch (ArgumentException)
        {
            result.error_code = "invalid_job";
            yield break;
        }
        string submissionJson;
        string wireJobId;
        var jobId = submission != null && submission.data != null
            ? submission.data.job_id
            : null;
        if (!TryExtractTopLevelObjectProperty(call.Text, "data", out submissionJson) ||
            !TryReadTopLevelProtocolIdProperty(
                submissionJson,
                "job_id",
                out wireJobId) ||
            !string.Equals(jobId, wireJobId, StringComparison.Ordinal))
        {
            result.error_code = "invalid_job";
            yield break;
        }
        result.job_id = jobId;
        try
        {
            // Persist the accepted 202 identity before polling or returning
            // control. A failed callback leaves the stable request resumable.
            if (persistJobId != null && !persistJobId(jobId))
            {
                result.error_code = "job_id_persistence_failed";
                yield break;
            }
        }
        catch (Exception)
        {
            result.error_code = "job_id_persistence_failed";
            yield break;
        }
        result.ok = true;
        result.error_code = "";
    }

    private IEnumerator Send(
        string method,
        string path,
        string json,
        long expectedStatus,
        CallResult result)
    {
        result.Ok = false;
        result.ErrorCode = "transport_failed";
        if (!IsConfigured)
        {
            result.ErrorCode = "invalid_endpoint";
            yield break;
        }

        using (var request = new UnityWebRequest(baseUrl + path, method))
        {
            if (json != null)
            {
                request.uploadHandler = new UploadHandlerRaw(Encoding.UTF8.GetBytes(json));
                request.SetRequestHeader("Content-Type", "application/json");
            }
            var capped = new CappedDownloadHandler(maxResponseBytes);
            request.downloadHandler = capped;
            request.SetRequestHeader("Accept", "application/json");
            if (!string.IsNullOrEmpty(token))
            {
                request.SetRequestHeader("Authorization", "Bearer " + token);
            }
            request.timeout = requestTimeoutSeconds;
            request.redirectLimit = 0;
            yield return request.SendWebRequest();

            if (capped.Exceeded)
            {
                result.ErrorCode = "response_too_large";
                yield break;
            }
            result.Text = capped.GetText();
            if (request.result != UnityWebRequest.Result.Success || request.responseCode != expectedStatus)
            {
                result.ErrorCode = SafeErrorCode(result.Text);
                yield break;
            }
            try
            {
                var envelope = JsonUtility.FromJson<BasicEnvelope>(result.Text);
                if (envelope == null || !envelope.ok)
                {
                    result.ErrorCode = envelope != null && envelope.error != null
                        ? SafeCode(envelope.error.code)
                        : "invalid_response";
                    yield break;
                }
            }
            catch (ArgumentException)
            {
                result.ErrorCode = "invalid_response";
                yield break;
            }
            result.Ok = true;
            result.ErrorCode = "";
        }
    }

    private static AdapterResult BuildOfflineResult(
        ProposeRequest request,
        string fallbackActionId,
        string reason,
        string jobId)
    {
        if (request == null || request.candidate_actions == null || request.candidate_actions.Length == 0)
        {
            return new AdapterResult
            {
                source = "error",
                committable = false,
                fallback_reason = "invalid_request",
                job_id = jobId,
            };
        }
        ActionSpec selected;
        if (string.IsNullOrEmpty(fallbackActionId))
        {
            selected = request.candidate_actions[0];
        }
        else
        {
            selected = request.candidate_actions.FirstOrDefault(
                action => action != null && action.id == fallbackActionId);
            if (selected == null)
            {
                return new AdapterResult
                {
                    source = "error",
                    committable = false,
                    fallback_reason = "invalid_fallback",
                    job_id = jobId ?? "",
                    proposal = null,
                };
            }
        }
        var stance = new[] { "engage", "partial", "redirect", "refuse", "wait" }.Contains(selected.kind)
            ? selected.kind
            : "engage";
        var canonical = JsonUtility.ToJson(request) + "\n" + selected.id;
        byte[] digest;
        using (var sha256 = SHA256.Create())
        {
            digest = sha256.ComputeHash(Encoding.UTF8.GetBytes(canonical));
        }
        var proposalId = "offline." + BitConverter.ToString(digest).Replace("-", "").ToLowerInvariant().Substring(0, 24);
        return new AdapterResult
        {
            source = "offline",
            committable = false,
            fallback_reason = SafeCode(reason),
            job_id = jobId ?? "",
            proposal = new ActionProposal
            {
                id = proposalId,
                session_id = request.session_id,
                request_id = request.request_id,
                actor_id = request.actor_id,
                tick = Math.Max(0, request.tick),
                action = selected,
                stance = stance,
                summary = "The game used its authored offline fallback.",
                rationale = "The Rin Sidecar was unavailable; world state remains game-owned.",
                policy_source = "adapter-offline",
                status = "offline",
            },
        };
    }

    private static AdapterResult ResolveCancellation(
        CallResult call,
        ProposeRequest request,
        string fallbackActionId,
        string jobId,
        bool allowConfirmedTerminalFallback,
        string unconfirmedReason)
    {
        if (!IsValidProtocolId(jobId))
            return BuildClosedResult("invalid_job", "");
        if (call == null || !call.Ok)
            return BuildClosedResult(unconfirmedReason, jobId);

        ProposalJob job;
        try
        {
            var envelope = JsonUtility.FromJson<JobEnvelope>(call.Text);
            job = envelope != null ? envelope.data : null;
        }
        catch (ArgumentException)
        {
            return BuildClosedResult("invalid_job", jobId);
        }
        if (job == null)
            return BuildClosedResult("invalid_job", jobId);
        if (!JobMatchesRequest(job, jobId, request))
            return BuildClosedResult("invalid_job_identity", jobId);
        if (!JobShapeMatchesStatus(job, call.Text))
            return BuildClosedResult("invalid_job", jobId);
        if (job.status == "succeeded")
        {
            if (job.proposal == null)
                return BuildClosedResult("invalid_job", jobId);
            if (!ProposalMatchesRequest(job.proposal, request, call.Text))
                return BuildClosedResult("invalid_job_identity", jobId);
            return new AdapterResult
            {
                source = "sidecar",
                committable = true,
                fallback_reason = "",
                job_id = jobId,
                proposal = job.proposal,
            };
        }
        if (job.status == "failed" || job.status == "stale" || job.status == "canceled")
        {
            string reason;
            if (!TryGetTerminalErrorCode(job, call.Text, out reason))
                return BuildClosedResult("job_outcome_unknown", jobId);
            return BuildTerminalResult(
                request,
                fallbackActionId,
                jobId,
                reason,
                allowConfirmedTerminalFallback);
        }
        if (job.status == "queued" || job.status == "running")
            return BuildClosedResult(unconfirmedReason, jobId);
        return BuildClosedResult("invalid_job", jobId);
    }

    private static bool JobMatchesRequest(
        ProposalJob job,
        string jobId,
        ProposeRequest request)
    {
        return job != null &&
            request != null &&
            string.Equals(
                job.protocol_version,
                ProtocolVersion,
                StringComparison.Ordinal) &&
            IsValidProtocolId(job.job_id) &&
            IsValidProtocolId(job.session_id) &&
            IsValidProtocolId(job.request_id) &&
            IsValidProtocolId(jobId) &&
            IsValidProtocolId(request.session_id) &&
            IsValidProtocolId(request.request_id) &&
            string.Equals(job.job_id, jobId, StringComparison.Ordinal) &&
            string.Equals(job.session_id, request.session_id, StringComparison.Ordinal) &&
            string.Equals(job.request_id, request.request_id, StringComparison.Ordinal);
    }

    private static bool JobShapeMatchesStatus(
        ProposalJob job,
        string responseJson)
    {
        string jobJson;
        string wireStatus;
        if (job == null ||
            !TryExtractTopLevelObjectProperty(responseJson, "data", out jobJson) ||
            !TryReadTopLevelProtocolIdProperty(jobJson, "status", out wireStatus) ||
            !string.Equals(job.status, wireStatus, StringComparison.Ordinal))
            return false;

        var proposalStart = FindTopLevelPropertyValue(jobJson, "proposal");
        var errorStart = FindTopLevelPropertyValue(jobJson, "error");
        if (job.status == "succeeded")
        {
            string proposalJson;
            return proposalStart >= 0 &&
                errorStart < 0 &&
                job.error == null &&
                job.proposal != null &&
                TryExtractTopLevelObjectProperty(
                    jobJson,
                    "proposal",
                    out proposalJson);
        }
        if (job.status == "failed" ||
            job.status == "stale" ||
            job.status == "canceled")
        {
            string reason;
            return proposalStart < 0 &&
                errorStart >= 0 &&
                job.proposal == null &&
                TryGetTerminalErrorCode(job, responseJson, out reason);
        }
        if (job.status == "queued" || job.status == "running")
        {
            return proposalStart < 0 &&
                errorStart < 0 &&
                job.proposal == null &&
                job.error == null;
        }
        return false;
    }

    private static bool ProposalMatchesRequest(
        ActionProposal proposal,
        ProposeRequest request,
        string responseJson)
    {
        string dataJson;
        string proposalJson;
        long wireTick;
        if (proposal == null ||
            request == null ||
            !TryExtractTopLevelObjectProperty(responseJson, "data", out dataJson) ||
            !TryExtractTopLevelObjectProperty(dataJson, "proposal", out proposalJson) ||
            !TryReadTopLevelInt64Property(proposalJson, "tick", out wireTick))
            return false;
        proposal.has_unsupported_action_parameters =
            ActionHasUnsupportedParameters(proposalJson);
        return !proposal.has_unsupported_action_parameters &&
            IsValidProtocolId(proposal.id) &&
            IsValidProtocolId(proposal.session_id) &&
            IsValidProtocolId(proposal.request_id) &&
            IsValidProtocolId(proposal.actor_id) &&
            IsValidProtocolId(request.session_id) &&
            IsValidProtocolId(request.request_id) &&
            IsValidProtocolId(request.actor_id) &&
            string.Equals(proposal.session_id, request.session_id, StringComparison.Ordinal) &&
            string.Equals(proposal.request_id, request.request_id, StringComparison.Ordinal) &&
            string.Equals(proposal.actor_id, request.actor_id, StringComparison.Ordinal) &&
            wireTick >= 0 &&
            request.tick >= 0 &&
            proposal.tick == wireTick &&
            wireTick == request.tick &&
            ActionMatchesCandidate(proposal.action, request.candidate_actions);
    }

    private static bool ActionHasUnsupportedParameters(string proposalJson)
    {
        string actionJson;
        // This example advertises no parameterized actions. Presence of the
        // protocol's arbitrary parameters map is therefore not representable
        // by JsonUtility and must fail closed instead of being silently dropped.
        return !TryExtractTopLevelObjectProperty(proposalJson, "action", out actionJson) ||
            FindTopLevelPropertyValue(actionJson, "parameters") >= 0;
    }

    private static bool ActionMatchesCandidate(
        ActionSpec action,
        ActionSpec[] candidates)
    {
        if (action == null ||
            candidates == null ||
            !IsValidProtocolId(action.id) ||
            !IsValidProtocolId(action.kind) ||
            string.IsNullOrWhiteSpace(action.description) ||
            action.description.Length > 300 ||
            !ValidTargetIds(action.target_ids))
            return false;
        return candidates.Any(candidate => ActionSpecsEqual(action, candidate));
    }

    private static bool ActionSpecsEqual(ActionSpec left, ActionSpec right)
    {
        if (left == null || right == null ||
            !IsValidProtocolId(right.id) ||
            !IsValidProtocolId(right.kind) ||
            string.IsNullOrWhiteSpace(right.description) ||
            right.description.Length > 300 ||
            !ValidTargetIds(right.target_ids) ||
            !string.Equals(left.id, right.id, StringComparison.Ordinal) ||
            !string.Equals(left.kind, right.kind, StringComparison.Ordinal) ||
            !string.Equals(left.description, right.description, StringComparison.Ordinal))
            return false;
        if (left.target_ids == null || right.target_ids == null)
            return left.target_ids == null && right.target_ids == null;
        return left.target_ids.SequenceEqual(right.target_ids);
    }

    private static bool ValidTargetIds(string[] values)
    {
        return values == null ||
            (values.Length <= 32 && values.All(IsValidProtocolId));
    }

    private static bool TryGetTerminalErrorCode(
        ProposalJob job,
        string responseJson,
        out string code)
    {
        code = null;
        string jobJson;
        string errorJson;
        string wireCode;
        if (job == null ||
            job.error == null ||
            !TryExtractTopLevelObjectProperty(responseJson, "data", out jobJson) ||
            !TryExtractTopLevelObjectProperty(jobJson, "error", out errorJson) ||
            !TryReadTopLevelProtocolIdProperty(errorJson, "code", out wireCode) ||
            !string.Equals(job.error.code, wireCode, StringComparison.Ordinal))
            return false;
        code = wireCode;
        return true;
    }

    private static bool IsValidProtocolId(string value)
    {
        if (string.IsNullOrEmpty(value) || value.Length > 96) return false;
        for (var index = 0; index < value.Length; index++)
        {
            var character = value[index];
            var alphaNumeric =
                (character >= 'A' && character <= 'Z') ||
                (character >= 'a' && character <= 'z') ||
                (character >= '0' && character <= '9');
            if (index == 0)
            {
                if (!alphaNumeric) return false;
            }
            else if (!alphaNumeric && character != '.' && character != '_' && character != '-')
            {
                return false;
            }
        }
        return true;
    }

    public static bool IsProtocolId(string value)
    {
        return IsValidProtocolId(value);
    }

    private static AdapterResult BuildTerminalResult(
        ProposeRequest request,
        string fallbackActionId,
        string jobId,
        string reason,
        bool allowFallback)
    {
        if (AmbiguousProposalErrors.Contains(reason))
            return BuildClosedResult(reason, jobId);
        return allowFallback
            ? BuildOfflineResult(request, fallbackActionId, reason, jobId)
            : BuildClosedResult(reason, jobId, "canceled");
    }

    private static AdapterResult BuildClosedResult(
        string reason,
        string jobId,
        string source = "error")
    {
        return new AdapterResult
        {
            source = source,
            committable = false,
            fallback_reason = SafeCode(reason),
            job_id = jobId ?? "",
            proposal = null,
        };
    }

    private static bool ValidateEndpoint(string value, string bearerToken)
    {
        Uri uri;
        if (!Uri.TryCreate(value, UriKind.Absolute, out uri) ||
            (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps) ||
            !string.IsNullOrEmpty(uri.UserInfo) ||
            uri.AbsolutePath != "/" ||
            !string.IsNullOrEmpty(uri.Query) ||
            !string.IsNullOrEmpty(uri.Fragment))
        {
            return false;
        }
        if (uri.Scheme == Uri.UriSchemeHttp && !uri.IsLoopback)
        {
            return false;
        }
        return uri.IsLoopback || !string.IsNullOrWhiteSpace(bearerToken);
    }

    private static string SafeErrorCode(string text)
    {
        try
        {
            var envelope = JsonUtility.FromJson<BasicEnvelope>(text);
            return envelope != null && envelope.error != null
                ? SafeCode(envelope.error.code)
                : "http_error";
        }
        catch (ArgumentException)
        {
            return "http_error";
        }
    }

    private static string SafeCode(string value)
    {
        if (string.IsNullOrEmpty(value)) return "unknown";
        var safe = new string(value.Take(96).Select(character =>
            char.IsLetterOrDigit(character) || character == '.' || character == '_' || character == '-'
                ? character
                : '_').ToArray());
        return safe;
    }

    // Unity's JsonUtility skips string-keyed maps. Extract only the requested
    // proposal object from SessionState.proposals, then deserialize that object
    // normally; no third-party JSON dependency is required for this example.
    private static bool TryExtractObjectProperty(
        string json,
        string containerName,
        string propertyName,
        out string objectJson)
    {
        objectJson = null;
        var containerStart = FindPropertyValue(json, containerName, 0, json.Length);
        if (containerStart < 0 || json[containerStart] != '{') return false;
        var containerEnd = FindMatchingContainer(json, containerStart);
        if (containerEnd < 0) return false;
        var valueStart = FindPropertyValue(
            json,
            propertyName,
            containerStart + 1,
            containerEnd);
        if (valueStart < 0 || json[valueStart] != '{') return false;
        var valueEnd = FindMatchingContainer(json, valueStart);
        if (valueEnd < 0 || valueEnd > containerEnd) return false;
        objectJson = json.Substring(valueStart, valueEnd - valueStart + 1);
        return true;
    }

    private static int FindPropertyValue(
        string json,
        string propertyName,
        int start,
        int end)
    {
        var needle = "\"" + EscapeJsonString(propertyName) + "\"";
        var search = start;
        while (search < end)
        {
            var property = json.IndexOf(needle, search, StringComparison.Ordinal);
            if (property < 0 || property >= end) return -1;
            var cursor = property + needle.Length;
            while (cursor < end && char.IsWhiteSpace(json[cursor])) cursor++;
            if (cursor < end && json[cursor] == ':')
            {
                cursor++;
                while (cursor < end && char.IsWhiteSpace(json[cursor])) cursor++;
                return cursor < end ? cursor : -1;
            }
            search = property + needle.Length;
        }
        return -1;
    }

    private static bool TryExtractTopLevelObjectProperty(
        string json,
        string propertyName,
        out string objectJson)
    {
        objectJson = null;
        var valueStart = FindTopLevelPropertyValue(json, propertyName);
        if (valueStart < 0 || json[valueStart] != '{') return false;
        var valueEnd = FindMatchingContainer(json, valueStart);
        if (valueEnd < 0) return false;
        objectJson = json.Substring(valueStart, valueEnd - valueStart + 1);
        return true;
    }

    private static bool TryReadTopLevelInt64Property(
        string json,
        string propertyName,
        out long value)
    {
        value = 0;
        var valueStart = FindTopLevelPropertyValue(json, propertyName);
        if (valueStart < 0) return false;

        var cursor = valueStart;
        if (cursor < json.Length && json[cursor] == '-') cursor++;
        var digitsStart = cursor;
        while (cursor < json.Length && char.IsDigit(json[cursor])) cursor++;
        if (cursor == digitsStart) return false;
        if (json[digitsStart] == '0' && cursor - digitsStart > 1) return false;

        var token = json.Substring(valueStart, cursor - valueStart);
        while (cursor < json.Length && char.IsWhiteSpace(json[cursor])) cursor++;
        if (cursor >= json.Length || (json[cursor] != ',' && json[cursor] != '}'))
            return false;
        return long.TryParse(
            token,
            NumberStyles.AllowLeadingSign,
            CultureInfo.InvariantCulture,
            out value);
    }

    private static bool TryReadTopLevelProtocolIdProperty(
        string json,
        string propertyName,
        out string value)
    {
        value = null;
        var valueStart = FindTopLevelPropertyValue(json, propertyName);
        if (valueStart < 0 || json[valueStart] != '"') return false;

        var cursor = valueStart + 1;
        var contentStart = cursor;
        while (cursor < json.Length && json[cursor] != '"')
        {
            // Protocol identifiers are ASCII and never require JSON escapes.
            if (json[cursor] == '\\') return false;
            cursor++;
        }
        if (cursor >= json.Length) return false;
        var decoded = json.Substring(contentStart, cursor - contentStart);
        cursor++;
        while (cursor < json.Length && char.IsWhiteSpace(json[cursor])) cursor++;
        if (cursor >= json.Length || (json[cursor] != ',' && json[cursor] != '}'))
            return false;
        if (!IsValidProtocolId(decoded)) return false;
        value = decoded;
        return true;
    }

    private static int FindTopLevelPropertyValue(
        string json,
        string propertyName)
    {
        if (string.IsNullOrEmpty(json)) return -1;
        var rootStart = 0;
        while (rootStart < json.Length && char.IsWhiteSpace(json[rootStart])) rootStart++;
        if (rootStart >= json.Length || json[rootStart] != '{') return -1;

        var needle = "\"" + EscapeJsonString(propertyName) + "\"";
        var depth = 0;
        for (var index = rootStart; index < json.Length; index++)
        {
            var character = json[index];
            if (character == '{' || character == '[')
            {
                depth++;
                continue;
            }
            if (character == '}' || character == ']')
            {
                depth--;
                if (depth <= 0) return -1;
                continue;
            }
            if (character != '"') continue;

            var stringStart = index;
            var escaped = false;
            for (index++; index < json.Length; index++)
            {
                character = json[index];
                if (escaped)
                {
                    escaped = false;
                }
                else if (character == '\\')
                {
                    escaped = true;
                }
                else if (character == '"')
                {
                    break;
                }
            }
            if (index >= json.Length) return -1;
            if (depth != 1 ||
                index - stringStart + 1 != needle.Length ||
                string.CompareOrdinal(
                    json,
                    stringStart,
                    needle,
                    0,
                    needle.Length) != 0)
            {
                continue;
            }

            var cursor = index + 1;
            while (cursor < json.Length && char.IsWhiteSpace(json[cursor])) cursor++;
            if (cursor >= json.Length || json[cursor] != ':') continue;
            cursor++;
            while (cursor < json.Length && char.IsWhiteSpace(json[cursor])) cursor++;
            return cursor < json.Length ? cursor : -1;
        }
        return -1;
    }

    private static int FindMatchingContainer(string json, int start)
    {
        var opener = json[start];
        var closer = opener == '{' ? '}' : opener == '[' ? ']' : '\0';
        if (closer == '\0') return -1;
        var depth = 0;
        var inString = false;
        var escaped = false;
        for (var index = start; index < json.Length; index++)
        {
            var character = json[index];
            if (inString)
            {
                if (escaped)
                {
                    escaped = false;
                }
                else if (character == '\\')
                {
                    escaped = true;
                }
                else if (character == '"')
                {
                    inString = false;
                }
                continue;
            }
            if (character == '"')
            {
                inString = true;
            }
            else if (character == opener)
            {
                depth++;
            }
            else if (character == closer && --depth == 0)
            {
                return index;
            }
        }
        return -1;
    }

    private static string EscapeJsonString(string value)
    {
        return (value ?? "").Replace("\\", "\\\\").Replace("\"", "\\\"");
    }

    private sealed class CallResult
    {
        public bool Ok;
        public string ErrorCode;
        public string Text;
    }

    private sealed class SubmissionAttempt
    {
        public bool ok;
        public string error_code;
        public string job_id;
    }

    private sealed class CappedDownloadHandler : DownloadHandlerScript
    {
        private readonly int maximum;
        private readonly MemoryStream stream = new MemoryStream();
        public bool Exceeded { get; private set; }

        public CappedDownloadHandler(int maximumBytes) : base(new byte[8192])
        {
            maximum = Math.Max(1024, Math.Min(32 * 1024 * 1024, maximumBytes));
        }

        protected override bool ReceiveData(byte[] data, int dataLength)
        {
            if (data == null || dataLength <= 0) return true;
            if (stream.Length + dataLength > maximum)
            {
                Exceeded = true;
                return false;
            }
            stream.Write(data, 0, dataLength);
            return true;
        }

        public string GetText()
        {
            return Encoding.UTF8.GetString(stream.ToArray());
        }
    }

    [Serializable] private sealed class BasicEnvelope { public bool ok; public ErrorDetail error; }
    [Serializable] private sealed class SubmissionEnvelope { public bool ok; public JobSubmission data; public ErrorDetail error; }
    [Serializable] private sealed class JobEnvelope { public bool ok; public ProposalJob data; public ErrorDetail error; }
    [Serializable] private sealed class MutationEnvelope { public bool ok; public MutationResult data; public ErrorDetail error; }
    [Serializable] private sealed class DueAgentsEnvelope { public bool ok; public DueAgentsResponse data; public ErrorDetail error; }
    [Serializable] private sealed class ArbitrationEnvelope { public bool ok; public ArbitrationResult data; public ErrorDetail error; }
    [Serializable] private sealed class TimelineEnvelope { public bool ok; public TimelineResponse data; public ErrorDetail error; }
    [Serializable] private sealed class ReplayEnvelope { public bool ok; public ReplaySnapshot data; public ErrorDetail error; }
    [Serializable] private sealed class StateEnvelope { public bool ok; public SessionStateHead data; public ErrorDetail error; }
}

[Serializable] public sealed class ActionSpec
{
    public string id;
    public string kind;
    public string description;
    public string[] target_ids;
}

[Serializable] public sealed class Binding
{
    public string game_id;
    public string content_id;
    public string content_version;
    public string content_hash;
}

[Serializable] public sealed class Boundary
{
    public string id;
    public string description;
    public string[] trigger_tags;
    public string response;
}

[Serializable] public sealed class Goal
{
    public string id;
    public string description;
    public string motivation;
    public int priority;
    public string[] preferred_actions;
    public int progress;
    public int target_progress;
    public string status;
    public long updated_tick;
    public long status_updated_tick;
    public string status_source_event_id;
    public long progress_accumulator;
    public bool status_explicit;
}

[Serializable] public sealed class ActorSeed
{
    public string id;
    public string kind;
    public string display_name;
    public string[] traits;
    public Boundary[] boundaries;
    public Goal[] goals;
    public long think_every_ticks;
    public bool enabled;
}

[Serializable] public sealed class CreateSessionRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string request_id;
    public string session_id;
    public Binding binding;
    public long seed;
    public string[] features;
    public ActorSeed[] actors;
}

[Serializable] public sealed class SessionRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string session_id;
}

[Serializable] public sealed class SessionStateHead
{
    public long revision;
    public long world_revision;
}

[Serializable] public sealed class ProposalFreshnessResult
{
    public long revision;
    public long world_revision;
    public ActionProposal proposal;
}

[Serializable] public sealed class ProposeRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string session_id;
    public string request_id;
    public string actor_id;
    public long tick;
    public string intent;
    public string[] tags;
    public ActionSpec[] candidate_actions;
    public Goal[] candidate_goals;
    public bool urgent;
}

[Serializable] public sealed class ObserveRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string session_id;
    public string request_id;
    public string event_id;
    public long tick;
    public string[] observer_ids;
    public string source;
    public string kind;
    public string summary;
    public string quote;
    public string[] tags;
    public int importance;
    public Fact[] facts;
}

[Serializable] public sealed class CommitRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string session_id;
    public string request_id;
    public string proposal_id;
    public string event_id;
    public long tick;
    public bool accepted;
    public string outcome;
    public string[] tags;
    public Fact[] facts;
    public GoalUpdate[] goal_updates;
}

[Serializable] public sealed class ActionProposal
{
    public string id;
    public string session_id;
    public string request_id;
    public string actor_id;
    public long tick;
    public long based_on_revision;
    public string based_on_head_hash;
    public long based_on_world_revision;
    public long created_revision;
    public ActionSpec action;
    public string stance;
    public string summary;
    public string rationale;
    // Audit/integration-only fields through proposed_goal; never render them directly.
    public string policy_source;
    public string[] recalled_memory_ids;
    public string goal_id;
    public string boundary_id;
    public Goal proposed_goal;
    public string status;
    public string outcome_event_id;
    public long outcome_tick;
    [NonSerialized] public bool has_unsupported_action_parameters;
}

[Serializable] public sealed class Fact
{
    public string subject_id;
    public string predicate;
    public string @object;
    public string[] visibility;
    public int confidence;
    public string source_event_id;
    public long observed_tick;
}

[Serializable] public sealed class GoalUpdate
{
    public string goal_id;
    public int progress_delta;
    public string status;
}

[Serializable] public sealed class CommitItem
{
    public string proposal_id;
    public string event_id;
    public bool accepted;
    public string outcome;
    public string[] tags;
    public Fact[] facts;
    public GoalUpdate[] goal_updates;
}

[Serializable] public sealed class BatchCommitRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string session_id;
    public string request_id;
    public long tick;
    public CommitItem[] items;
}

[Serializable] public sealed class ActorActivityUpdate
{
    public string actor_id;
    public string region_id;
    public string state;
    public string reason;
}

[Serializable] public sealed class SetActorActivityRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string session_id;
    public string request_id;
    public long tick;
    public ActorActivityUpdate[] updates;
}

[Serializable] public sealed class DueAgentsRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string session_id;
    public long tick;
    public int limit;
    public string[] region_ids;
}

[Serializable] public sealed class DueAgent
{
    public string actor_id;
    public long next_think_tick;
    public string region_id;
}

[Serializable] public sealed class DueAgentsResponse
{
    public string session_id;
    public long tick;
    public DueAgent[] agents;
}

[Serializable] public sealed class ArbitrateRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string session_id;
    public string request_id;
    public long tick;
    public string[] proposal_ids;
    public string[] exclusive_target_ids;
}

[Serializable] public sealed class ArbitrationDecision
{
    public string proposal_id;
    public string actor_id;
    public string status;
    public string reason;
    public string[] conflicting_proposal_ids;
}

[Serializable] public sealed class ArbitrationRecord
{
    public string id;
    public string request_id;
    public long tick;
    public long based_on_world_revision;
    public long created_revision;
    public ArbitrationDecision[] decisions;
}

[Serializable] public sealed class ArbitrationResult
{
    public ArbitrationRecord record;
    public bool duplicate;
}

[Serializable] public sealed class TimelineRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string session_id;
    public long after_revision;
    public int limit = 50;
}

[Serializable] public sealed class TimelineEntry
{
    public long sequence;
    public string type;
    public string request_id;
    public string recorded_at;
    public string hash;
    public string prev_hash;
    public string[] entity_ids;
    public string[] actor_ids;
    public string status;
}

[Serializable] public sealed class TimelineResponse
{
    public string session_id;
    public long current_revision;
    public TimelineEntry[] entries;
    public long next_after_revision;
    public bool has_more;
}

[Serializable] public sealed class ReplayRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string session_id;
    public long revision;
}

[Serializable] public sealed class ReplayStateHeader
{
    public string protocol_version;
    public string session_id;
    public long tick;
    public long revision;
    public long world_revision;
    public string head_hash;
    public string[] features;
}

[Serializable] public sealed class ReplaySnapshot
{
    public string protocol_version;
    public string state_hash;
    public ReplayStateHeader state;
}

[Serializable] public sealed class ErrorDetail
{
    public string code;
    public string message;
    public string field;
}

[Serializable] public sealed class JobSubmission
{
    public string protocol_version;
    public string job_id;
    public string status;
    public bool duplicate;
}

[Serializable] public sealed class ProposalJob
{
    public string protocol_version;
    public string job_id;
    public string session_id;
    public string request_id;
    public string status;
    public ActionProposal proposal;
    public ErrorDetail error;
}

[Serializable] public sealed class MutationResult
{
    public string session_id;
    public long revision;
    public string head_hash;
    public bool duplicate;
}

[Serializable] public sealed class ReportAttempt
{
    public bool ok;
    public string error_code;
    public MutationResult data;
}

[Serializable] public sealed class AdapterResult
{
    public string source;
    public bool committable;
    public string fallback_reason;
    public string job_id;
    public ActionProposal proposal;
}
