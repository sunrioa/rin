using System;

internal static class RinUnityClockAuthority
{
    private const long MaxJsonInteger = 9007199254740991L;

    public static bool PendingIsValid(PendingTurnState value)
    {
        var window = value.request.decision_window;
        return window.opened_at != null &&
            window.deadline != null &&
            value.authoritative_clock == window.deadline.clock &&
            value.authoritative_clock_value >= window.opened_at.value &&
            value.authoritative_clock_value <= MaxJsonInteger;
    }

    public static bool RestorePending(PendingTurnState value)
    {
        if (value == null || value.request == null ||
            value.request.decision_window == null ||
            value.request.decision_window.opened_at == null)
        {
            return false;
        }
        if (string.IsNullOrEmpty(value.authoritative_clock))
        {
            value.authoritative_clock =
                value.request.decision_window.opened_at.clock;
            value.authoritative_clock_value =
                value.request.decision_window.opened_at.value;
        }
        return true;
    }

    public static bool DeadlineAllowsStart(
        PendingTurnState pending,
        ActionInvocation invocation)
    {
        return pending != null && invocation != null &&
            invocation.deadline != null &&
            pending.authoritative_clock == invocation.deadline.clock &&
            pending.authoritative_clock_value >= 0 &&
            pending.authoritative_clock_value <= invocation.deadline.value;
    }

    public static bool TryObserve(
        PendingTurnState pending,
        ActiveRunState active,
        string clock,
        long value,
        out bool pendingChanged,
        out bool activeExpired)
    {
        pendingChanged = false;
        activeExpired = false;
        if ((clock != "event" && clock != "step" && clock != "realtime") ||
            value < 0 || value > MaxJsonInteger)
        {
            return false;
        }
        if (pending != null && pending.authoritative_clock == clock &&
            value > pending.authoritative_clock_value)
        {
            pending.authoritative_clock_value = value;
            pendingChanged = true;
        }
        var deadline = active?.invocation?.deadline;
        activeExpired = deadline != null &&
            deadline.clock == clock && value > deadline.value;
        return true;
    }

    public static bool Observe(
        bool stateReady,
        PendingTurnState pending,
        ActiveRunState active,
        string clock,
        long value,
        Func<bool> persist,
        Action<string> replaceAuthority)
    {
        if (!stateReady || !TryObserve(
            pending, active, clock, value,
            out var pendingChanged, out var activeExpired))
        {
            return stateReady;
        }
        if (pendingChanged && !persist())
        {
            replaceAuthority(
                "The Unity Host could not persist its authoritative clock.");
            return false;
        }
        if (activeExpired)
        {
            replaceAuthority(
                "The Unity action exceeded its game-authored deadline.");
        }
        return true;
    }
}
