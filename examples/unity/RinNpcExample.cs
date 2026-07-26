using UnityEngine;

// Game-owned host code stays small: capture an event, ask the reusable
// coordinator to run one turn, and keep all actual world effects authoritative.
public sealed class RinNpcExample : MonoBehaviour, IRinUnityHost
{
    [SerializeField] private RinUnityWorkflow workflow = null;

    private void Awake()
    {
        if (workflow != null) workflow.ConfigureHost(this);
    }

    public void AskNpcToRespond()
    {
        if (workflow == null)
        {
            Debug.LogError("RinUnityWorkflow is not assigned.");
            return;
        }
        workflow.RequestTurn();
    }

    public RinTurnInput CaptureTurn(long operationSequence, Epoch epoch)
    {
        var opened = operationSequence * 100;
        return new RinTurnInput
        {
            actor_id = "npc.guide",
            tick = operationSequence,
            observation_seq = operationSequence,
            opened_at = opened,
            deadline = opened + 50,
            intent = "Respond to the player's latest interaction.",
            observation_summary = "The player asked the guide to respond.",
            offers = new[]
            {
                new RinActionOfferTemplate
                {
                    capability = new CapabilityRef
                    {
                        id = "dialogue.say",
                        version = "1.0.0",
                    },
                    descriptor_digest =
                        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                    description = "Say the authored greeting to the player.",
                    arguments_json = "{\"text\":\"Welcome, traveler.\"}",
                },
            },
        };
    }

    public RinHostActionResult Execute(ActionInvocation invocation)
    {
        if (invocation.capability.id != "dialogue.say")
        {
            return RinHostActionResult.Rejected("Unsupported example capability.");
        }
        // A real game resolves the already-authored arguments and performs the
        // effect here. For world mutation, persist operation_id in game state.
        Debug.Log("Rin selected dialogue.say: " + invocation.argumentsJson);
        return new RinHostActionResult
        {
            accepted = true,
            summary = "The guide delivered the selected line.",
            world_seq = invocation.observation_seq + 1,
            occurred_at = invocation.deadline.value,
        };
    }
}
