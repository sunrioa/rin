internal static class RinUnityOfferBinding
{
    public static bool Matches(ActionOffer left, ActionOffer right)
    {
        if (left == null || right == null ||
            left.offer_id != right.offer_id ||
            left.decision_window_id != right.decision_window_id ||
            left.actor_id != right.actor_id ||
            left.capability == null || right.capability == null ||
            left.capability.id != right.capability.id ||
            left.capability.version != right.capability.version ||
            left.descriptor_digest != right.descriptor_digest ||
            left.description != right.description ||
            left.observation_seq != right.observation_seq ||
            !EpochEquals(left.expected_epoch, right.expected_epoch) ||
            !TimepointEquals(left.deadline, right.deadline))
        {
            return false;
        }
        // JsonUtility keeps response arguments opaque. Execution always uses
        // the durable candidate's raw arguments, never the response copy.
        return TargetsEqual(left.targets, right.targets);
    }

    public static bool InvocationMatches(
        ActionInvocation invocation,
        ActionOffer offer,
        string argumentsJson)
    {
        return invocation != null && offer != null &&
            invocation.offer_id == offer.offer_id &&
            invocation.decision_window_id == offer.decision_window_id &&
            invocation.actor_id == offer.actor_id &&
            invocation.capability != null && offer.capability != null &&
            invocation.capability.id == offer.capability.id &&
            invocation.capability.version == offer.capability.version &&
            invocation.descriptor_digest == offer.descriptor_digest &&
            invocation.argumentsJson == argumentsJson &&
            invocation.observation_seq == offer.observation_seq &&
            EpochEquals(invocation.expected_epoch, offer.expected_epoch) &&
            TimepointEquals(invocation.deadline, offer.deadline) &&
            TargetsEqual(invocation.targets, offer.targets);
    }

    public static bool DecisionWindowEquals(
        DecisionWindow left,
        DecisionWindow right)
    {
        if (left == null || right == null ||
            left.id != right.id ||
            left.mode != right.mode ||
            left.observation_seq != right.observation_seq ||
            !EpochEquals(left.epoch, right.epoch) ||
            !TimepointEquals(left.opened_at, right.opened_at) ||
            !TimepointEquals(left.deadline, right.deadline))
        {
            return false;
        }
        var leftActors = left.actor_ids ?? new string[0];
        var rightActors = right.actor_ids ?? new string[0];
        if (leftActors.Length != rightActors.Length) return false;
        for (var index = 0; index < leftActors.Length; index++)
        {
            if (leftActors[index] != rightActors[index]) return false;
        }
        return true;
    }

    public static bool EpochEquals(Epoch left, Epoch right)
    {
        return left != null && right != null &&
            left.session_id == right.session_id &&
            left.world_id == right.world_id &&
            left.host == right.host &&
            left.world == right.world &&
            left.timeline == right.timeline;
    }

    public static ActionInvocation Invocation(string operationId, ActionOffer offer)
    {
        return new ActionInvocation
        {
            operation_id = operationId,
            offer_id = offer.offer_id,
            decision_window_id = offer.decision_window_id,
            actor_id = offer.actor_id,
            capability = offer.capability,
            descriptor_digest = offer.descriptor_digest,
            argumentsJson = offer.argumentsJson,
            targets = offer.targets,
            expected_epoch = offer.expected_epoch.Copy(),
            observation_seq = offer.observation_seq,
            deadline = offer.deadline.Copy(),
        };
    }

    private static bool HostRefEquals(HostRef left, HostRef right)
    {
        return left != null && right != null &&
            left.@namespace == right.@namespace &&
            left.type == right.type &&
            left.key == right.key &&
            left.ephemeral == right.ephemeral &&
            EpochEquals(left.epoch, right.epoch);
    }

    private static bool TargetsEqual(HostRef[] left, HostRef[] right)
    {
        left = left ?? new HostRef[0];
        right = right ?? new HostRef[0];
        if (left.Length != right.Length) return false;
        for (var index = 0; index < left.Length; index++)
        {
            if (!HostRefEquals(left[index], right[index])) return false;
        }
        return true;
    }

    private static bool TimepointEquals(Timepoint left, Timepoint right)
    {
        return left != null && right != null &&
            left.clock == right.clock &&
            left.value == right.value;
    }
}
