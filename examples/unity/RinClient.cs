using System;
using System.Collections;
using System.IO;
using System.Net;
using System.Text;
using UnityEngine;
using UnityEngine.Networking;

// Dependency-free Unity 2021+ transport for Rin Protocol v2. Calls are
// coroutines, so network I/O never blocks the render or simulation thread.
public sealed class RinClient : MonoBehaviour
{
    public const string ProtocolVersion = "rin.protocol/v2";

    [SerializeField] private string baseUrl = "http://127.0.0.1:7374";
    [SerializeField] private string tokenEnvironment = "RIN_TOKEN";
    [SerializeField, Range(1, 120)] private int requestTimeoutSeconds = 10;
    [SerializeField] private int maxResponseBytes = 32 * 1024 * 1024;

    private string token = "";

    public bool IsConfigured { get; private set; }
    public string LastErrorCode { get; private set; }

    private void Awake()
    {
        baseUrl = (baseUrl ?? "").Trim().TrimEnd('/');
        token = Environment.GetEnvironmentVariable(tokenEnvironment) ?? "";
        IsConfigured = ValidateEndpoint(baseUrl, token);
        LastErrorCode = "";
        if (!IsConfigured)
        {
            Debug.LogError("Rin client is disabled because its endpoint is invalid.");
        }
    }

    public IEnumerator CreateSession(
        CreateSessionRequest request,
        Action<MutationResult> completed)
    {
        var call = new RinCall();
        yield return Send("/v2/session/create", JsonUtility.ToJson(request), call);
        completed(DecodeMutation(call));
    }

    public IEnumerator Observe(
        ObserveRequest request,
        Action<MutationResult> completed)
    {
        var call = new RinCall();
        yield return Send("/v2/session/observe", JsonUtility.ToJson(request), call);
        completed(DecodeMutation(call));
    }

    public IEnumerator Propose(
        ProposeRequest request,
        Action<ProposalResult> completed)
    {
        string body;
        try
        {
            body = RinUnityJson.SerializePropose(request);
        }
        catch (ArgumentException error)
        {
            LastErrorCode = "invalid_arguments_json";
            Debug.LogError(error.Message);
            completed(null);
            yield break;
        }

        var call = new RinCall();
        yield return Send("/v2/agent/propose", body, call);
        if (!call.ok)
        {
            completed(null);
            yield break;
        }
        try
        {
            var envelope = JsonUtility.FromJson<ProposalEnvelope>(call.text);
            if (envelope == null || !envelope.ok || envelope.data == null ||
                envelope.data.proposal == null)
            {
                LastErrorCode = "invalid_response";
                completed(null);
                yield break;
            }
            completed(envelope.data);
        }
        catch (Exception error)
        {
            LastErrorCode = "invalid_response";
            Debug.LogError("Rin proposal response is invalid: " + error.Message);
            completed(null);
        }
    }

    public IEnumerator ReportAction(
        ReportActionRequest request,
        Action<MutationResult> completed)
    {
        string body;
        try
        {
            body = RinUnityJson.SerializeReport(request);
        }
        catch (ArgumentException error)
        {
            LastErrorCode = "invalid_arguments_json";
            Debug.LogError(error.Message);
            completed(null);
            yield break;
        }

        var call = new RinCall();
        yield return Send("/v2/action/report", body, call);
        completed(DecodeMutation(call));
    }

    private MutationResult DecodeMutation(RinCall call)
    {
        if (!call.ok) return null;
        try
        {
            var envelope = JsonUtility.FromJson<MutationEnvelope>(call.text);
            if (envelope != null && envelope.ok && envelope.data != null)
            {
                return envelope.data;
            }
        }
        catch (Exception error)
        {
            Debug.LogError("Rin mutation response is invalid: " + error.Message);
        }
        LastErrorCode = "invalid_response";
        return null;
    }

    private IEnumerator Send(string path, string body, RinCall call)
    {
        if (!IsConfigured)
        {
            call.errorCode = LastErrorCode = "invalid_endpoint";
            yield break;
        }
        if (Encoding.UTF8.GetByteCount(body) > maxResponseBytes)
        {
            call.errorCode = LastErrorCode = "request_too_large";
            yield break;
        }

        var response = new BoundedDownloadHandler(maxResponseBytes);
        using (var request = new UnityWebRequest(baseUrl + path, "POST"))
        {
            request.uploadHandler = new UploadHandlerRaw(Encoding.UTF8.GetBytes(body));
            request.downloadHandler = response;
            request.timeout = requestTimeoutSeconds;
            request.redirectLimit = 0;
            request.SetRequestHeader("Accept", "application/json");
            request.SetRequestHeader("Content-Type", "application/json; charset=utf-8");
            if (token.Length != 0)
            {
                request.SetRequestHeader("Authorization", "Bearer " + token);
            }

            yield return request.SendWebRequest();
            if (request.result != UnityWebRequest.Result.Success ||
                request.responseCode != 200 ||
                response.overflowed)
            {
                call.errorCode = LastErrorCode = response.overflowed
                    ? "response_too_large"
                    : DecodeError(response.Text, "transport_failed");
                yield break;
            }
            call.ok = true;
            call.text = response.Text;
            LastErrorCode = "";
        }
    }

    private static string DecodeError(string text, string fallback)
    {
        try
        {
            var envelope = JsonUtility.FromJson<ErrorEnvelope>(text);
            if (envelope != null && envelope.error != null &&
                RinUnityIds.IsValid(envelope.error.code))
            {
                return envelope.error.code;
            }
        }
        catch (Exception)
        {
            // The transport error remains the useful failure category.
        }
        return fallback;
    }

    private static bool ValidateEndpoint(string value, string bearerToken)
    {
        Uri uri;
        if (!Uri.TryCreate(value, UriKind.Absolute, out uri) ||
            !string.IsNullOrEmpty(uri.UserInfo) ||
            !string.IsNullOrEmpty(uri.Query) ||
            !string.IsNullOrEmpty(uri.Fragment))
        {
            return false;
        }
        var loopback = string.Equals(uri.Host, "localhost", StringComparison.OrdinalIgnoreCase);
        IPAddress address;
        if (IPAddress.TryParse(uri.Host, out address))
        {
            loopback = IPAddress.IsLoopback(address);
        }
        if (uri.Scheme == Uri.UriSchemeHttp)
        {
            return loopback;
        }
        return uri.Scheme == Uri.UriSchemeHttps &&
            (loopback || !string.IsNullOrEmpty(bearerToken));
    }

    private sealed class RinCall
    {
        public bool ok;
        public string text = "";
        public string errorCode = "";
    }

    private sealed class BoundedDownloadHandler : DownloadHandlerScript
    {
        private readonly MemoryStream stream = new MemoryStream();
        private readonly int limit;
        public bool overflowed;

        public BoundedDownloadHandler(int limit) : base(new byte[16 * 1024])
        {
            this.limit = limit;
        }

        public string Text
        {
            get { return Encoding.UTF8.GetString(stream.ToArray()); }
        }

        protected override bool ReceiveData(byte[] data, int dataLength)
        {
            if (data == null || dataLength <= 0) return true;
            if (stream.Length + dataLength > limit)
            {
                overflowed = true;
                return false;
            }
            stream.Write(data, 0, dataLength);
            return true;
        }

        public override void Dispose()
        {
            stream.Dispose();
            base.Dispose();
        }
    }
}

internal static class RinUnityJson
{
    public static bool IsValidObject(string raw)
    {
        try
        {
            return JsonUtility.FromJson<RinOpaqueJson>(
                NormalizeObject(raw)) != null;
        }
        catch (Exception)
        {
            return false;
        }
    }

    public static string SerializePropose(ProposeRequest request)
    {
        if (request == null || request.offers == null)
        {
            throw new ArgumentException("Rin ProposeRequest must contain action offers.");
        }
        var arguments = new string[request.offers.Length];
        for (var index = 0; index < request.offers.Length; index++)
        {
            arguments[index] = request.offers[index].argumentsJson;
        }
        return InjectArguments(JsonUtility.ToJson(request), arguments);
    }

    public static string SerializeReport(ReportActionRequest request)
    {
        if (request == null || request.report == null ||
            request.report.invocation == null)
        {
            return JsonUtility.ToJson(request);
        }
        return InjectArguments(
            JsonUtility.ToJson(request),
            new[] { request.report.invocation.argumentsJson });
    }

    private static string InjectArguments(string json, string[] arguments)
    {
        const string marker = "\"arguments\":{}";
        var cursor = 0;
        for (var index = 0; index < arguments.Length; index++)
        {
            var raw = NormalizeObject(arguments[index]);
            var position = json.IndexOf(marker, cursor, StringComparison.Ordinal);
            if (position < 0)
            {
                throw new ArgumentException(
                    "Unity JSON serialization did not preserve the arguments placeholder.");
            }
            var valueStart = position + "\"arguments\":".Length;
            json = json.Substring(0, valueStart) + raw +
                json.Substring(position + marker.Length);
            cursor = valueStart + raw.Length;
        }
        if (json.IndexOf(marker, cursor, StringComparison.Ordinal) >= 0)
        {
            throw new ArgumentException("An action argument payload was not supplied.");
        }
        return json;
    }

    private static string NormalizeObject(string raw)
    {
        raw = (raw ?? "").Trim();
        if (raw.Length < 2 || raw.Length > 64 * 1024 ||
            raw[0] != '{' || raw[raw.Length - 1] != '}' ||
            raw.IndexOf('\0') >= 0)
        {
            throw new ArgumentException(
                "Action arguments must be a JSON object no larger than 64 KiB.");
        }
        return raw;
    }
}

internal static class RinUnityIds
{
    public static bool IsDigest(string value)
    {
        if (value == null || value.Length != 64) return false;
        foreach (var character in value)
        {
            if (!((character >= '0' && character <= '9') ||
                (character >= 'a' && character <= 'f')))
            {
                return false;
            }
        }
        return true;
    }

    public static bool IsValid(string value)
    {
        if (string.IsNullOrEmpty(value) || value.Length > 128 ||
            value[0] < 'a' || value[0] > 'z')
        {
            return false;
        }
        var separator = false;
        for (var index = 1; index < value.Length; index++)
        {
            var character = value[index];
            var alphanumeric =
                (character >= 'a' && character <= 'z') ||
                (character >= '0' && character <= '9');
            if (alphanumeric)
            {
                separator = false;
                continue;
            }
            if ((character == '.' || character == '_' || character == '-') &&
                !separator)
            {
                separator = true;
                continue;
            }
            return false;
        }
        return !separator;
    }
}

[Serializable]
public sealed class RinBinding
{
    public string game_id;
    public string content_id;
    public string content_version;
    public string content_hash;
}

[Serializable]
public sealed class ActorSeed
{
    public string id;
    public string kind;
    public string display_name;
    public int think_every_ticks;
    public bool enabled = true;
    public string[] traits = new string[0];
}

[Serializable]
public sealed class Epoch
{
    public string session_id;
    public string world_id;
    public long host;
    public long world;
    public long timeline;

    public Epoch Copy()
    {
        return new Epoch
        {
            session_id = session_id,
            world_id = world_id,
            host = host,
            world = world,
            timeline = timeline,
        };
    }
}

[Serializable]
public sealed class Timepoint
{
    public string clock;
    public long value;

    public Timepoint Copy()
    {
        return new Timepoint { clock = clock, value = value };
    }
}

[Serializable]
public sealed class CapabilityRef
{
    public string id;
    public string version;
}

[Serializable]
public sealed class HostRef
{
    public string @namespace;
    public string type;
    public string key;
    public bool ephemeral;
    public Epoch epoch;
}

[Serializable]
public sealed class DecisionWindow
{
    public string id;
    public string mode;
    public Epoch epoch;
    public long observation_seq;
    public Timepoint opened_at;
    public Timepoint deadline;
    public string[] actor_ids;
}

[Serializable]
public sealed class RinOpaqueJson
{
}

[Serializable]
public sealed class ActionOffer
{
    public string offer_id;
    public string decision_window_id;
    public string actor_id;
    public CapabilityRef capability;
    public string descriptor_digest;
    public string description;
    public RinOpaqueJson arguments = new RinOpaqueJson();
    [NonSerialized] public string argumentsJson = "{}";
    public HostRef[] targets = new HostRef[0];
    public Epoch expected_epoch;
    public long observation_seq;
    public Timepoint deadline;
}

[Serializable]
public sealed class CreateSessionRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string request_id;
    public string session_id;
    public RinBinding binding;
    public ActorSeed[] actors;
    public string[] features = new string[0];
}

[Serializable]
public sealed class ObserveRequest
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
    public string[] tags = new string[0];
    public int importance;
    public Epoch epoch;
    public long observation_seq;
}

[Serializable]
public sealed class ProposeRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string session_id;
    public string request_id;
    public string actor_id;
    public long tick;
    public string intent;
    public string[] tags = new string[0];
    public DecisionWindow decision_window;
    public ActionOffer[] offers;
}

[Serializable]
public sealed class ActionProposal
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
    public DecisionWindow decision_window;
    public ActionOffer action;
    public string stance;
    public string summary;
    public string rationale;
    public string status;
}

[Serializable]
public sealed class ActionInvocation
{
    public string operation_id;
    public string offer_id;
    public string decision_window_id;
    public string actor_id;
    public CapabilityRef capability;
    public string descriptor_digest;
    public RinOpaqueJson arguments = new RinOpaqueJson();
    [NonSerialized] public string argumentsJson = "{}";
    public HostRef[] targets = new HostRef[0];
    public Epoch expected_epoch;
    public long observation_seq;
    public Timepoint deadline;
}

[Serializable]
public sealed class ActionRun
{
    public string operation_id;
    public string status;
    public long progress_seq;
    public int progress;
    public Timepoint updated_at;
    public string message;
}

[Serializable]
public sealed class ActionOutcome
{
    public string operation_id;
    public string status;
    public string summary;
    public string code;
    public Epoch epoch;
    public long world_seq;
    public Timepoint occurred_at;
    public HostRef[] evidence = new HostRef[0];
}

[Serializable]
public sealed class ActionReport
{
    public string proposal_id;
    public string event_id;
    public string decision;
    public ActionInvocation invocation;
    public ActionRun run;
    public ActionOutcome outcome;
    public string summary;
    public string[] tags = new string[0];
}

[Serializable]
public sealed class ReportActionRequest
{
    public string protocol_version = RinClient.ProtocolVersion;
    public string session_id;
    public string request_id;
    public long tick;
    public ActionReport report;
}

[Serializable]
public sealed class MutationResult
{
    public string session_id;
    public long revision;
    public string head_hash;
    public bool duplicate;
}

[Serializable]
public sealed class ProposalResult
{
    public ActionProposal proposal;
    public bool duplicate;
}

[Serializable]
internal sealed class MutationEnvelope
{
    public bool ok = false;
    public MutationResult data = null;
}

[Serializable]
internal sealed class ProposalEnvelope
{
    public bool ok = false;
    public ProposalResult data = null;
}

[Serializable]
internal sealed class RinApiError
{
    public string code = null;
    public string message = null;
}

[Serializable]
internal sealed class ErrorEnvelope
{
    public bool ok = false;
    public RinApiError error = null;
}
