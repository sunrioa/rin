using System;
using System.Collections;
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

    [SerializeField] private string baseUrl = "http://127.0.0.1:7374";
    [SerializeField] private string token = "";
    [SerializeField, Range(1, 120)] private int requestTimeoutSeconds = 5;
    [SerializeField, Range(1, 300)] private int jobDeadlineSeconds = 25;
    [SerializeField, Range(0.05f, 5f)] private float pollIntervalSeconds = 0.1f;
    [SerializeField] private int maxResponseBytes = 2 * 1024 * 1024;

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

    public IEnumerator Commit(CommitRequest request, Action<MutationResult> completed)
    {
        var call = new CallResult();
        yield return Send("POST", "/v1/action/commit", JsonUtility.ToJson(request), 200, call);
        completed(call.Ok ? JsonUtility.FromJson<MutationEnvelope>(call.Text).data : null);
    }

    public IEnumerator ProposeWithFallback(
        ProposeRequest request,
        string fallbackActionId,
        Action<AdapterResult> completed,
        Func<bool> isCanceled = null)
    {
        if (!IsConfigured)
        {
            completed(BuildOfflineResult(request, fallbackActionId, "invalid_endpoint", ""));
            yield break;
        }

        var submissionCall = new CallResult();
        yield return Send(
            "POST",
            "/v1/jobs/propose",
            JsonUtility.ToJson(request),
            202,
            submissionCall);
        if (!submissionCall.Ok)
        {
            completed(BuildOfflineResult(request, fallbackActionId, submissionCall.ErrorCode, ""));
            yield break;
        }

        var submission = JsonUtility.FromJson<SubmissionEnvelope>(submissionCall.Text);
        var jobId = submission != null && submission.data != null ? submission.data.job_id : "";
        if (string.IsNullOrEmpty(jobId))
        {
            completed(BuildOfflineResult(request, fallbackActionId, "invalid_submission", ""));
            yield break;
        }

        var deadline = Time.realtimeSinceStartup + jobDeadlineSeconds;
        while (Time.realtimeSinceStartup < deadline)
        {
            if (isCanceled != null && isCanceled())
            {
                var cancelCall = new CallResult();
                yield return Send("DELETE", "/v1/jobs/" + UnityWebRequest.EscapeURL(jobId), null, 200, cancelCall);
                completed(new AdapterResult
                {
                    source = "canceled",
                    committable = false,
                    fallback_reason = "job_canceled",
                    job_id = jobId,
                    proposal = null,
                });
                yield break;
            }

            var pollCall = new CallResult();
            yield return Send("GET", "/v1/jobs/" + UnityWebRequest.EscapeURL(jobId), null, 200, pollCall);
            if (!pollCall.Ok)
            {
                completed(BuildOfflineResult(request, fallbackActionId, pollCall.ErrorCode, jobId));
                yield break;
            }
            var envelope = JsonUtility.FromJson<JobEnvelope>(pollCall.Text);
            var job = envelope != null ? envelope.data : null;
            if (job == null)
            {
                completed(BuildOfflineResult(request, fallbackActionId, "invalid_job", jobId));
                yield break;
            }
            if (job.status == "succeeded" && job.proposal != null)
            {
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
                var reason = job.error != null && !string.IsNullOrEmpty(job.error.code)
                    ? job.error.code
                    : "job_" + job.status;
                completed(BuildOfflineResult(request, fallbackActionId, reason, jobId));
                yield break;
            }
            if (job.status != "queued" && job.status != "running")
            {
                completed(BuildOfflineResult(request, fallbackActionId, "invalid_job", jobId));
                yield break;
            }
            yield return new WaitForSecondsRealtime(pollIntervalSeconds);
        }

        var timeoutCancel = new CallResult();
        yield return Send("DELETE", "/v1/jobs/" + UnityWebRequest.EscapeURL(jobId), null, 200, timeoutCancel);
        completed(BuildOfflineResult(request, fallbackActionId, "job_timeout", jobId));
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
        var selected = request.candidate_actions.FirstOrDefault(
            action => action != null && action.id == fallbackActionId) ?? request.candidate_actions[0];
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

    private sealed class CallResult
    {
        public bool Ok;
        public string ErrorCode;
        public string Text;
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
    public ActorSeed[] actors;
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
    public long created_revision;
    public ActionSpec action;
    public string stance;
    public string summary;
    public string rationale;
    public string policy_source;
    public string[] recalled_memory_ids;
    public string goal_id;
    public string status;
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

[Serializable] public sealed class AdapterResult
{
    public string source;
    public bool committable;
    public string fallback_reason;
    public string job_id;
    public ActionProposal proposal;
}
