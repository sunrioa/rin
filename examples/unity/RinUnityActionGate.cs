using System;
using System.Threading;

// The game owns capture and execution. BeginAction may complete synchronously
// or later, but it must return a handle whose Cancel result states whether the
// terminal cancellation outcome is known.
public interface IRinUnityHost
{
    RinTurnInput CaptureTurn(long operationSequence, Epoch epoch);
    IRinUnityAction BeginAction(
        ActionInvocation invocation,
        Action<RinHostActionResult> completed);
}

public interface IRinUnityAction
{
    // True means the action is now terminal and known-cancelled. False means
    // the effect may have happened and must be reported outcome-unknown.
    bool Cancel();
}

public static class RinUnityAction
{
    public static readonly IRinUnityAction Completed = new CompletedAction();

    private sealed class CompletedAction : IRinUnityAction
    {
        public bool Cancel()
        {
            return true;
        }
    }
}

// Process-local authority gate. Every callback is bound to one generation;
// authority replacement terminates the active action before advancing.
public sealed class RinUnityActionGate
{
    private long generation;
    private long activeGeneration;
    private IRinUnityAction active;
    private Action<RinHostActionResult> terminal;

    public bool IsActive
    {
        get { return terminal != null; }
    }

    public void Begin(
        Func<Action<RinHostActionResult>, IRinUnityAction> start,
        Action<RinHostActionResult> completed,
        Action<Action> dispatch)
    {
        if (start == null || completed == null || dispatch == null)
        {
            throw new ArgumentNullException("Unity action gate callback");
        }
        if (IsActive)
        {
            throw new InvalidOperationException("A Unity Host action is already active.");
        }
        if (generation == long.MaxValue)
        {
            throw new InvalidOperationException("Unity action generation is exhausted.");
        }

        var token = ++generation;
        var completionClaimed = 0;
        activeGeneration = token;
        terminal = completed;
        IRinUnityAction handle;
        try
        {
            handle = start(result =>
            {
                if (Interlocked.Exchange(ref completionClaimed, 1) == 0)
                {
                    dispatch(() => Complete(token, result));
                }
            });
        }
        catch (Exception error)
        {
            Complete(
                token,
                RinHostActionResult.Rejected(
                    "The Unity Host failed to start the action: " +
                    error.GetType().Name));
            return;
        }

        if (activeGeneration == token && terminal != null)
        {
            active = handle;
            if (active == null)
            {
                Complete(
                    token,
                    RinHostActionResult.Rejected(
                        "The Unity Host returned no action handle."));
            }
        }
    }

    public void ReplaceAuthority(string summary)
    {
        if (!IsActive) return;
        var knownCancelled = active != null && SafeCancel(active);
        Complete(
            activeGeneration,
            new RinHostActionResult
            {
                accepted = true,
                status = knownCancelled ? "cancelled" : "outcome-unknown",
                summary = summary,
            });
    }

    private void Complete(long token, RinHostActionResult result)
    {
        if (token != activeGeneration || terminal == null) return;
        var callback = terminal;
        active = null;
        terminal = null;
        callback(result ?? RinHostActionResult.Rejected(
            "The Unity Host returned no action result."));
    }

    private static bool SafeCancel(IRinUnityAction handle)
    {
        try
        {
            return handle.Cancel();
        }
        catch (Exception)
        {
            return false;
        }
    }
}
