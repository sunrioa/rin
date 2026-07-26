using System;
using UnityEngine;
using UnityEngine.AI;

// Game-owned host code stays small: capture an event, ask the reusable
// coordinator to run one turn, and keep all actual world effects authoritative.
public sealed class RinNpcExample : MonoBehaviour, IRinUnityHost
{
    [SerializeField] private RinUnityWorkflow workflow = null;
    [SerializeField] private NavMeshAgent agent = null;
    [SerializeField] private Transform authoredDestination = null;

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
                    offer_id = "offer.move_to.authored_destination",
                    capability = new CapabilityRef
                    {
                        id = "movement.move_to",
                        version = "1.0.0",
                    },
                    descriptor_digest =
                        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                    description = "Walk to the game-authored destination.",
                    arguments_json = "{\"destination_id\":\"guide_marker\"}",
                },
            },
        };
    }

    public IRinUnityAction BeginAction(
        ActionInvocation invocation,
        Action<RinHostActionResult> completed)
    {
        if (invocation.capability.id != "movement.move_to" ||
            invocation.offer_id != "offer.move_to.authored_destination")
        {
            completed(RinHostActionResult.Rejected(
                "Unsupported example capability."));
            return RinUnityAction.Completed;
        }
        return RinNavMeshAction.Begin(
            agent,
            authoredDestination,
            invocation,
            completed);
    }
}
