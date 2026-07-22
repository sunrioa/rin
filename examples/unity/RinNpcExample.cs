using System.Collections;
using UnityEngine;

public sealed class RinNpcExample : MonoBehaviour
{
    [SerializeField] private RinClient rin;

    public void AskNpcToRespond()
    {
        StartCoroutine(ProposeAndApply());
    }

    private IEnumerator ProposeAndApply()
    {
        MutationResult created = null;
        yield return rin.CreateSession(new CreateSessionRequest
        {
            request_id = "create.playthrough-1",
            session_id = "playthrough-1",
            binding = new Binding
            {
                game_id = "example-game",
                content_id = "base",
                content_version = "1.0.0",
                content_hash = "example-content-hash",
            },
            seed = 42,
            actors = new[]
            {
                new ActorSeed
                {
                    id = "npc.mira",
                    kind = "npc",
                    display_name = "Mira",
                    traits = new[] { "careful" },
                    goals = new[]
                    {
                        new Goal
                        {
                            id = "goal.connect",
                            description = "Build trust through specific actions.",
                            priority = 4,
                            preferred_actions = new[] { "talk" },
                            progress = 0,
                            target_progress = 3,
                            status = "active",
                        },
                    },
                    think_every_ticks = 5,
                    enabled = true,
                },
            },
        }, value => created = value);
        if (created == null) yield break;

        var request = new ProposeRequest
        {
            session_id = "playthrough-1",
            request_id = "propose.turn-19.mira",
            actor_id = "npc.mira",
            tick = 19,
            intent = "Choose how to respond to the player.",
            tags = new[] { "conversation", "trust" },
            candidate_actions = new[]
            {
                new ActionSpec { id = "talk", kind = "dialogue", description = "Ask one honest question." },
                new ActionSpec { id = "wait", kind = "wait", description = "Stay silent for now." },
            },
        };
        AdapterResult result = null;
        yield return rin.ProposeWithFallback(request, "wait", value => result = value);
        if (result == null || result.proposal == null) yield break;

        ApplyActionInGame(result.proposal.action);
        if (result.committable)
        {
            yield return rin.Commit(new CommitRequest
            {
                session_id = request.session_id,
                request_id = "commit.turn-19.mira",
                proposal_id = result.proposal.id,
                event_id = "event.turn-19.mira",
                tick = request.tick,
                accepted = true,
                outcome = "The game applied the advertised action.",
            }, _ => { });
        }
    }

    private void ApplyActionInGame(ActionSpec action)
    {
        // Replace with navigation, animation, dialogue, or combat owned by Unity.
        Debug.Log("Apply game-owned action: " + action.id);
    }
}
