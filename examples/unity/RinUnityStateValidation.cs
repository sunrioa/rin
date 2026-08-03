using System;
using System.Collections.Generic;

internal static class RinUnityStateValidation
{

    public static bool Pending(
        string runId,
        string sessionId,
        PendingTurnState value)
    {
        if (value.version != 1 ||
            !RinUnityIds.IsValid(value.operation_id) ||
            !value.operation_id.StartsWith(
                "unity.operation." + runId + ".",
                StringComparison.Ordinal) ||
            value.observation == null ||
            value.request == null ||
            value.request.decision_window == null ||
            value.request.offers == null ||
            value.request.offers.Length == 0 ||
            value.request.offers.Length > 32 ||
            value.offer_arguments_json == null ||
            value.offer_arguments_json.Length != value.request.offers.Length ||
            !RinUnityClockAuthority.PendingIsValid(value) ||
            value.observation.session_id != sessionId ||
            value.request.session_id != sessionId ||
            value.observation.tick != value.request.tick ||
            value.observation.observation_seq !=
                value.request.decision_window.observation_seq ||
            !RinUnityOfferBinding.EpochEquals(
                value.observation.epoch,
                value.request.decision_window.epoch))
        {
            return false;
        }
        var offerIds = new HashSet<string>();
        foreach (var offer in value.request.offers)
        {
            if (offer == null ||
                !RinUnityIds.IsValid(offer.offer_id) ||
                !offerIds.Add(offer.offer_id) ||
                offer.decision_window_id != value.request.decision_window.id ||
                offer.actor_id != value.request.actor_id ||
                offer.capability == null ||
                !RinUnityIds.IsValid(offer.capability.id) ||
                !RinUnityIds.IsDigest(offer.descriptor_digest) ||
                !RinUnityOfferBinding.EpochEquals(
                    offer.expected_epoch,
                    value.request.decision_window.epoch) ||
                offer.observation_seq !=
                    value.request.decision_window.observation_seq)
            {
                return false;
            }
        }
        return true;
    }

    public static bool Active(
        PendingTurnState pending,
        ActiveRunState value)
    {
        if (!RinUnityIds.IsValid(value.operation_id) ||
            value.proposal == null ||
            value.invocation == null ||
            value.invocation.operation_id != value.operation_id ||
            !RinUnityJson.IsValidObject(value.arguments_json) ||
            value.proposal.session_id != pending.request.session_id ||
            value.proposal.request_id != pending.request.request_id ||
            value.proposal.actor_id != pending.request.actor_id ||
            value.proposal.tick != pending.request.tick ||
            !RinUnityOfferBinding.DecisionWindowEquals(
                value.proposal.decision_window,
                pending.request.decision_window))
        {
            return false;
        }
        foreach (var offer in pending.request.offers)
        {
            if (RinUnityOfferBinding.Matches(offer, value.proposal.action) &&
                RinUnityOfferBinding.InvocationMatches(
                    value.invocation,
                    offer,
                    value.arguments_json))
            {
                return true;
            }
        }
        return false;
    }

    public static bool RestorePendingArguments(PendingTurnState value)
    {
        for (var index = 0; index < value.request.offers.Length; index++)
        {
            var arguments = value.offer_arguments_json[index];
            if (!RinUnityJson.IsValidObject(arguments)) return false;
            value.request.offers[index].argumentsJson = arguments;
        }
        return true;
    }

    public static void RestoreArguments(AppliedMarker marker)
    {
        if (marker.request.report != null &&
            marker.request.report.invocation != null)
        {
            marker.request.report.invocation.argumentsJson =
                marker.arguments_json;
        }
    }

    public static void RestoreArguments(ReportOutboxEntry entry)
    {
        if (entry.request.report != null &&
            entry.request.report.invocation != null)
        {
            entry.request.report.invocation.argumentsJson =
                entry.arguments_json;
        }
    }

}
