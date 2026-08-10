package cognition

import (
	"context"
	"errors"

	rinruntime "github.com/sunrioa/rin/runtime"
)

type Failover struct {
	Primary    rinruntime.DecisionProvider
	Fallback   rinruntime.DecisionProvider
	OnFallback func(error)
}

func (p Failover) Propose(ctx context.Context, input rinruntime.DecisionContext) (rinruntime.DecisionDraft, error) {
	if err := requireContext(ctx); err != nil {
		return rinruntime.DecisionDraft{}, err
	}
	if p.Primary == nil || p.Fallback == nil {
		return rinruntime.DecisionDraft{}, errors.New("primary and fallback policies are required")
	}
	draft, err := p.Primary.Propose(ctx, input)
	if err == nil {
		return draft, nil
	}
	if ctx.Err() != nil {
		return rinruntime.DecisionDraft{}, ctx.Err()
	}
	if errors.Is(err, rinruntime.ErrNoSafeAction) {
		return rinruntime.DecisionDraft{}, err
	}
	if p.OnFallback != nil {
		p.OnFallback(err)
	}
	draft, fallbackError := p.Fallback.Propose(ctx, input)
	if fallbackError != nil {
		return rinruntime.DecisionDraft{}, fallbackError
	}
	draft.PolicySource = "deterministic-fallback"
	return draft, nil
}
