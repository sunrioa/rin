using UnityEngine;

// Game-owned host code stays small: capture an event, ask the reusable
// coordinator to run one turn, and keep all actual world effects authoritative.
public sealed class RinNpcExample : MonoBehaviour
{
    [SerializeField] private RinUnityWorkflow workflow;

    public void AskNpcToRespond()
    {
        if (workflow == null)
        {
            Debug.LogError("RinUnityWorkflow is not assigned.");
            return;
        }
        workflow.RequestTurn();
    }
}
