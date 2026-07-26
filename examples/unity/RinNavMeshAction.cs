using System;
using UnityEngine;
using UnityEngine.AI;

// Reference long-running gameplay action. Navigation and completion remain
// game-owned; Rin only selects an already-authored movement offer.
public sealed class RinNavMeshAction : MonoBehaviour, IRinUnityAction
{
    private NavMeshAgent agent;
    private ActionInvocation invocation;
    private Action<RinHostActionResult> completed;
    private bool terminal;

    public static IRinUnityAction Begin(
        NavMeshAgent agent,
        Transform destination,
        ActionInvocation invocation,
        Action<RinHostActionResult> completed)
    {
        if (agent == null || destination == null || invocation == null)
        {
            completed(RinHostActionResult.Rejected(
                "The authored NavMesh destination is unavailable."));
            return RinUnityAction.Completed;
        }
        if (!agent.isOnNavMesh)
        {
            completed(RinHostActionResult.Rejected(
                "The NavMesh agent could not start the authored route."));
            return RinUnityAction.Completed;
        }

        var action = agent.gameObject.AddComponent<RinNavMeshAction>();
        action.agent = agent;
        action.invocation = invocation;
        action.completed = completed;
        if (!agent.SetDestination(destination.position))
        {
            action.terminal = true;
            action.completed = null;
            Destroy(action);
            completed(RinHostActionResult.Rejected(
                "The NavMesh agent could not start the authored route."));
            return RinUnityAction.Completed;
        }
        return action;
    }

    private void Update()
    {
        if (terminal || agent == null || agent.pathPending) return;
        if (agent.hasPath && agent.remainingDistance > agent.stoppingDistance) return;
        Finish(new RinHostActionResult
        {
            accepted = true,
            status = "succeeded",
            summary = "The NavMesh agent reached the authored destination.",
            world_seq = invocation.observation_seq + 1,
            occurred_at = invocation.deadline.value,
        });
    }

    private void OnDisable()
    {
        if (!terminal)
        {
            Finish(new RinHostActionResult
            {
                accepted = true,
                status = "outcome-unknown",
                summary = "The NavMesh action was disabled before a terminal result.",
            });
        }
    }

    public bool Cancel()
    {
        if (terminal) return true;
        terminal = true;
        if (agent != null && agent.isOnNavMesh) agent.ResetPath();
        completed = null;
        Destroy(this);
        return true;
    }

    private void Finish(RinHostActionResult result)
    {
        if (terminal) return;
        terminal = true;
        var callback = completed;
        completed = null;
        if (callback != null) callback(result);
        Destroy(this);
    }
}
