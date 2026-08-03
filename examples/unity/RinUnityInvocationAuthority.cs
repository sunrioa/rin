internal static class RinUnityInvocationAuthority
{
    public static string StartError(
        PendingTurnState pending,
        ActionInvocation invocation,
        Epoch currentEpoch)
    {
        if (!RinUnityOfferBinding.EpochEquals(
            invocation.expected_epoch,
            currentEpoch))
        {
            return "The Unity Host rejected an offer from a replaced authority.";
        }
        if (!RinUnityClockAuthority.DeadlineAllowsStart(pending, invocation))
        {
            return "The Unity Host rejected an offer after its deadline.";
        }
        return null;
    }
}
