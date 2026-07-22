package policy

import (
	"context"
	"errors"

	rinruntime "github.com/sunrioa/rin/runtime"
)

type Failover struct {
	Primary    rinruntime.Policy
	Fallback   rinruntime.Policy
	OnFallback func(error)
}

func (p Failover) Propose(ctx context.Context, input rinruntime.PolicyContext) (rinruntime.ProposalDraft, error) {
	if p.Primary == nil || p.Fallback == nil {
		return rinruntime.ProposalDraft{}, errors.New("primary and fallback policies are required")
	}
	draft, err := p.Primary.Propose(ctx, input)
	if err == nil {
		return draft, nil
	}
	if ctx.Err() != nil {
		return rinruntime.ProposalDraft{}, ctx.Err()
	}
	if errors.Is(err, rinruntime.ErrNoSafeAction) {
		return rinruntime.ProposalDraft{}, err
	}
	if p.OnFallback != nil {
		p.OnFallback(err)
	}
	draft, fallbackError := p.Fallback.Propose(ctx, input)
	if fallbackError != nil {
		return rinruntime.ProposalDraft{}, fallbackError
	}
	draft.PolicySource = "deterministic-fallback"
	return draft, nil
}
