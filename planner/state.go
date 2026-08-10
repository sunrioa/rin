package planner

import "fmt"

const maxControlTransitions = MaxNodes * (MaxLoopIterations + 2)

type controlParent struct {
	id   string
	kind NodeKind
	path string
}

// ValidateState rejects forged or internally inconsistent execution state.
func (p Plan) ValidateState(state State) error {
	if state.Steps > p.Budget.MaxSteps ||
		state.WorldMutations > p.Budget.MaxWorldMutations {
		return fmt.Errorf("plan state exceeds its budget")
	}
	if !state.Started && (state.StartedAt != 0 || state.Tick != 0) {
		return fmt.Errorf("plan state has ticks before it started")
	}
	if !state.Started && (state.Steps != 0 || state.WorldMutations != 0 ||
		len(state.Completed) != 0 || len(state.Skipped) != 0 || len(state.Failed) != 0 ||
		len(state.Attempts) != 0 || len(state.Loops) != 0 || len(state.Branches) != 0 ||
		len(state.ActiveLoops) != 0) {
		return fmt.Errorf("plan state has progress before it started")
	}
	if state.Started && state.Tick < state.StartedAt {
		return fmt.Errorf("plan state tick precedes its start")
	}
	if state.Started && state.Tick-state.StartedAt > p.Budget.MaxTicks {
		return fmt.Errorf("plan state exceeds its tick budget")
	}
	parents := p.controlParents()
	for id, completed := range state.Completed {
		node, ok := p.node(id)
		if !completed || !ok || state.Skipped[id] || state.Failed[id] ||
			node.Kind == Action && state.Attempts[id] == 0 ||
			node.Kind == Branch && state.Branches[id] == "" {
			return fmt.Errorf("invalid completed node %q", id)
		}
	}
	for id, skipped := range state.Skipped {
		if _, ok := p.node(id); !skipped || !ok || !p.skipAuthorized(state, id, parents) {
			return fmt.Errorf("invalid skipped node %q", id)
		}
	}
	for id, failed := range state.Failed {
		node, ok := p.node(id)
		if !failed || !ok || node.Kind != Action || state.Completed[id] || state.Skipped[id] ||
			state.Attempts[id] != node.MaxAttempts {
			return fmt.Errorf("invalid failed node %q", id)
		}
	}
	for id, attempts := range state.Attempts {
		node, ok := p.node(id)
		if !ok || node.Kind != Action || attempts == 0 || attempts > node.MaxAttempts ||
			attempts == node.MaxAttempts && !state.Completed[id] && !state.Failed[id] {
			return fmt.Errorf("invalid attempts for %q", id)
		}
	}
	for id, iterations := range state.Loops {
		node, ok := p.node(id)
		if !ok || node.Kind != Loop || iterations > node.MaxIterations {
			return fmt.Errorf("invalid loop state for %q", id)
		}
	}
	for id, path := range state.Branches {
		node, ok := p.node(id)
		if !ok || node.Kind != Branch || (path != "then" && path != "else") ||
			!state.Completed[id] || state.Skipped[id] {
			return fmt.Errorf("invalid branch state for %q", id)
		}
	}
	for id, active := range state.ActiveLoops {
		node, ok := p.node(id)
		if !ok || node.Kind != Loop || !active || state.Completed[id] ||
			state.Skipped[id] || state.Loops[id] >= node.MaxIterations {
			return fmt.Errorf("invalid active loop %q", id)
		}
	}
	return nil
}

// Advance resolves all currently ready branch and loop control nodes. It never
// executes an action or mutates a game world.
func (p Plan) Advance(state State, facts map[string]bool, tick uint64) (State, error) {
	if err := p.Validate(); err != nil {
		return state, err
	}
	if err := p.ValidateState(state); err != nil {
		return state, err
	}
	if stateFailed(state) {
		return cloneState(state), nil
	}
	next, err := p.startedState(state, tick)
	if err != nil {
		return state, err
	}
	parents := p.controlParents()
	for transitions := 0; transitions < maxControlTransitions; transitions++ {
		changed := false

		for _, node := range p.Nodes {
			if node.Kind != Loop || !next.ActiveLoops[node.ID] ||
				!p.controlSubtreesDone(next, node.Children) {
				continue
			}
			next.Loops[node.ID]++
			delete(next.ActiveLoops, node.ID)
			changed = true
		}

		for _, node := range p.Nodes {
			if node.Kind != Branch || next.Completed[node.ID] || next.Skipped[node.ID] ||
				!p.controlEnabled(next, node.ID, parents) ||
				!dependenciesDone(next, node.DependsOn) {
				continue
			}
			path := "else"
			selected := node.Else
			unselected := node.Then
			if conditionsTrue(node.When, facts) {
				path = "then"
				selected = node.Then
				unselected = node.Else
			}
			next.Branches[node.ID] = path
			next.Completed[node.ID] = true
			for _, child := range selected {
				p.resetSubtree(&next, child)
			}
			for _, child := range unselected {
				p.skipSubtree(&next, child)
			}
			changed = true
		}

		for _, node := range p.Nodes {
			if node.Kind != Loop || next.Completed[node.ID] || next.Skipped[node.ID] ||
				next.ActiveLoops[node.ID] || !p.controlEnabled(next, node.ID, parents) ||
				!dependenciesDone(next, node.DependsOn) {
				continue
			}
			if next.Loops[node.ID] >= node.MaxIterations ||
				!conditionsTrue(node.When, facts) {
				next.Completed[node.ID] = true
			} else {
				next.ActiveLoops[node.ID] = true
				for _, child := range node.Children {
					p.resetSubtree(&next, child)
				}
			}
			changed = true
		}

		if !changed {
			if err := p.ValidateState(next); err != nil {
				return state, err
			}
			return next, nil
		}
	}
	return state, fmt.Errorf("plan control transitions exceeded their bound")
}

// Fail records one verified failed attempt. The action remains eligible until
// its attempt budget is exhausted.
func (p Plan) Fail(state State, nodeID string, tick uint64, worldMutations uint32) (State, error) {
	return p.recordAction(state, nodeID, tick, worldMutations, false)
}

// Done reports whether the plan reached either a successful or failed terminal state.
func (p Plan) Done(state State) bool {
	if p.Validate() != nil || p.ValidateState(state) != nil {
		return false
	}
	if stateFailed(state) {
		return true
	}
	parents := p.controlParents()
	for _, node := range p.Nodes {
		if _, controlled := parents[node.ID]; !controlled && !p.subtreeDone(state, node.ID) {
			return false
		}
	}
	return true
}

// Succeeded reports whether the plan completed every required root without an
// exhausted action.
func (p Plan) Succeeded(state State) bool {
	return p.Done(state) && !stateFailed(state)
}

func (p Plan) recordAction(
	state State,
	nodeID string,
	tick uint64,
	worldMutations uint32,
	succeeded bool,
) (State, error) {
	if err := p.Allows(state, nodeID, tick, worldMutations); err != nil {
		return state, err
	}
	next, err := p.startedState(state, tick)
	if err != nil {
		return state, err
	}
	node, _ := p.node(nodeID)
	next.Attempts[nodeID]++
	if succeeded {
		next.Completed[nodeID] = true
	} else if next.Attempts[nodeID] == node.MaxAttempts {
		next.Failed[nodeID] = true
	}
	next.Steps++
	next.WorldMutations += worldMutations
	next.Tick = tick
	return next, nil
}

func (p Plan) startedState(state State, tick uint64) (State, error) {
	next := cloneState(state)
	if !next.Started {
		next.Started = true
		next.StartedAt = tick
		next.Tick = tick
		return next, nil
	}
	if tick < next.Tick {
		return state, fmt.Errorf("plan tick moved backwards")
	}
	if tick-next.StartedAt > p.Budget.MaxTicks {
		return state, fmt.Errorf("plan tick budget exceeded")
	}
	next.Tick = tick
	return next, nil
}

func cloneState(state State) State {
	return State{
		Completed:      cloneBoolMap(state.Completed),
		Skipped:        cloneBoolMap(state.Skipped),
		Failed:         cloneBoolMap(state.Failed),
		Attempts:       cloneUintMap(state.Attempts),
		Loops:          cloneUintMap(state.Loops),
		Branches:       cloneStringMap(state.Branches),
		ActiveLoops:    cloneBoolMap(state.ActiveLoops),
		Steps:          state.Steps,
		WorldMutations: state.WorldMutations,
		Started:        state.Started,
		StartedAt:      state.StartedAt,
		Tick:           state.Tick,
	}
}

func (p Plan) controlParents() map[string]controlParent {
	parents := make(map[string]controlParent)
	for _, node := range p.Nodes {
		for _, child := range node.Then {
			parents[child] = controlParent{id: node.ID, kind: Branch, path: "then"}
		}
		for _, child := range node.Else {
			parents[child] = controlParent{id: node.ID, kind: Branch, path: "else"}
		}
		for _, child := range node.Children {
			parents[child] = controlParent{id: node.ID, kind: Loop}
		}
	}
	return parents
}

func (p Plan) controlEnabled(
	state State,
	nodeID string,
	parents map[string]controlParent,
) bool {
	parent, controlled := parents[nodeID]
	if !controlled {
		return true
	}
	if !p.controlEnabled(state, parent.id, parents) {
		return false
	}
	if parent.kind == Branch {
		return state.Branches[parent.id] == parent.path
	}
	return state.ActiveLoops[parent.id]
}

func (p Plan) skipAuthorized(
	state State,
	nodeID string,
	parents map[string]controlParent,
) bool {
	current := nodeID
	for {
		parent, controlled := parents[current]
		if !controlled {
			return false
		}
		if parent.kind == Branch {
			selected := state.Branches[parent.id]
			if selected != "" && selected != parent.path {
				return true
			}
		}
		current = parent.id
	}
}

func (p Plan) controlSubtreesDone(state State, roots []string) bool {
	for _, root := range roots {
		if !p.subtreeDone(state, root) {
			return false
		}
	}
	return true
}

func (p Plan) subtreeDone(state State, nodeID string) bool {
	if state.Skipped[nodeID] {
		return true
	}
	node, ok := p.node(nodeID)
	if !ok || !state.Completed[nodeID] {
		return false
	}
	switch node.Kind {
	case Branch:
		path := state.Branches[node.ID]
		if path == "then" {
			return p.controlSubtreesDone(state, node.Then)
		}
		if path == "else" {
			return p.controlSubtreesDone(state, node.Else)
		}
		return false
	case Loop:
		return !state.ActiveLoops[node.ID]
	default:
		return true
	}
}

func (p Plan) resetSubtree(state *State, nodeID string) {
	node, ok := p.node(nodeID)
	if !ok {
		return
	}
	delete(state.Completed, nodeID)
	delete(state.Skipped, nodeID)
	delete(state.Failed, nodeID)
	delete(state.Attempts, nodeID)
	delete(state.Branches, nodeID)
	delete(state.ActiveLoops, nodeID)
	delete(state.Loops, nodeID)
	for _, child := range append(append(append([]string{}, node.Then...), node.Else...), node.Children...) {
		p.resetSubtree(state, child)
	}
}

func (p Plan) skipSubtree(state *State, nodeID string) {
	node, ok := p.node(nodeID)
	if !ok {
		return
	}
	p.resetSubtree(state, nodeID)
	state.Skipped[nodeID] = true
	for _, child := range append(append(append([]string{}, node.Then...), node.Else...), node.Children...) {
		p.skipSubtree(state, child)
	}
}

func dependenciesDone(state State, dependencies []string) bool {
	for _, dependency := range dependencies {
		if !state.Completed[dependency] && !state.Skipped[dependency] {
			return false
		}
	}
	return true
}

func conditionsTrue(conditions []string, facts map[string]bool) bool {
	for _, condition := range conditions {
		if !facts[condition] {
			return false
		}
	}
	return true
}

func stateFailed(state State) bool {
	for _, failed := range state.Failed {
		if failed {
			return true
		}
	}
	return false
}
