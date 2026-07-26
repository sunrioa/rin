internal static class RinUnityReportValidation
{
    private const long MaxJsonInteger = 9007199254740991L;

    public static bool Marker(string sessionId, AppliedMarker marker)
    {
        return marker != null &&
            marker.operation_id != null &&
            marker.proposal_id != null &&
            marker.request != null &&
            marker.request.report != null &&
            marker.proposal_id == marker.request.report.proposal_id &&
            Report(sessionId, marker.operation_id, marker.arguments_json, marker.request);
    }

    public static bool Outbox(string sessionId, ReportOutboxEntry entry)
    {
        return entry != null &&
            Report(sessionId, entry.key, entry.arguments_json, entry.request);
    }

    private static bool Report(
        string sessionId,
        string operationId,
        string argumentsJson,
        ReportActionRequest request)
    {
        if (!RinUnityIds.IsValid(operationId) ||
            request == null ||
            request.report == null ||
            request.session_id != sessionId ||
            !RinUnityIds.IsValid(request.request_id) ||
            request.tick < 0 || request.tick > MaxJsonInteger ||
            !RinUnityIds.IsValid(request.report.proposal_id) ||
            !RinUnityIds.IsValid(request.report.event_id) ||
            !RinUnityJson.IsValidObject(argumentsJson))
        {
            return false;
        }
        if (request.report.decision == "rejected")
        {
            return request.report.invocation == null &&
                request.report.run == null &&
                request.report.outcome == null;
        }
        var invocation = request.report.invocation;
        var run = request.report.run;
        var outcome = request.report.outcome;
        return request.report.decision == "accepted" &&
            invocation != null &&
            run != null &&
            outcome != null &&
            invocation.operation_id == operationId &&
            run.operation_id == operationId &&
            outcome.operation_id == operationId &&
            run.progress_seq > 0 &&
            run.progress >= 0 && run.progress <= 100 &&
            Terminal(run.status) &&
            run.status == outcome.status &&
            RinUnityOfferBinding.EpochEquals(
                invocation.expected_epoch,
                outcome.epoch);
    }

    private static bool Terminal(string status)
    {
        return status == "succeeded" ||
            status == "failed" ||
            status == "cancelled" ||
            status == "interrupted" ||
            status == "stale" ||
            status == "outcome-unknown";
    }
}
