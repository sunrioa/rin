package runtime

import (
	"fmt"

	"github.com/sunrioa/rin/protocol"
)

// playerFacingProposalText is the single information-flow gate for proposal
// presentation. Its inputs contain no private goal, boundary, memory, belief,
// prompt, provider, or Policy Draft text.
func playerFacingProposalText(action protocol.ActionSpec, stance string) (string, string) {
	summary := fmt.Sprintf("Proposes: %s", action.Description)
	switch stance {
	case "partial":
		return summary, "Selects a limited action currently allowed by the game."
	case "redirect":
		return summary, "Selects a game-authorized redirection."
	case "refuse":
		return summary, "Selects a game-authorized refusal."
	case "wait":
		return summary, "Selects a game-authorized wait action."
	default:
		return summary, "Selected from the actions currently allowed by the game."
	}
}

func canonicalizeProposalPresentation(proposal *protocol.ActionProposal) {
	proposal.Summary, proposal.Rationale = playerFacingProposalText(
		proposal.Action,
		proposal.Stance,
	)
}

func canonicalizeStateProposalPresentation(state *protocol.SessionState) {
	for proposalID, proposal := range state.Proposals {
		canonicalizeProposalPresentation(&proposal)
		state.Proposals[proposalID] = proposal
	}
	for actorID, actor := range state.Actors {
		for index := range actor.RecentActions {
			canonicalizeProposalPresentation(&actor.RecentActions[index])
		}
		state.Actors[actorID] = actor
	}
}

func canonicalizeIdentifierProposalPresentation(history *protocol.IdentifierHistory) {
	for requestID, identity := range history.Requests {
		if identity.Proposal == nil {
			continue
		}
		proposal := *identity.Proposal
		canonicalizeProposalPresentation(&proposal)
		identity.Proposal = &proposal
		history.Requests[requestID] = identity
	}
}
