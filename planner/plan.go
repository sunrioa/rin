// Package planner contains the engine-neutral shape and scheduler for bounded
// host-authored task plans. It never resolves targets or mutates a world.
package planner

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

const (
	CurrentSchemaVersion uint32 = 1
	MaxNodes                    = 64
	MaxDepth                    = 16
	MaxConditionLen             = 160
	MaxConditions               = 16
	MaxAttempts                 = 8
	MaxLoopIterations           = 32
)

type NodeKind string

const (
	Action NodeKind = "action"
	Branch NodeKind = "branch"
	Loop   NodeKind = "loop"
)

type Node struct {
	ID             string   `json:"id"`
	Kind           NodeKind `json:"kind"`
	Capability     string   `json:"capability,omitempty"`
	DependsOn      []string `json:"depends_on,omitempty"`
	When           []string `json:"when,omitempty"`
	Then           []string `json:"then,omitempty"`
	Else           []string `json:"else,omitempty"`
	Children       []string `json:"children,omitempty"`
	Priority       int      `json:"priority,omitempty"`
	MaxAttempts    uint32   `json:"max_attempts,omitempty"`
	MaxIterations  uint32   `json:"max_iterations,omitempty"`
	WorldMutations uint32   `json:"world_mutations,omitempty"`
	Risk           string   `json:"risk"`
}

type Budget struct {
	MaxSteps          uint32 `json:"max_steps"`
	MaxWorldMutations uint32 `json:"max_world_mutations"`
	MaxTicks          uint64 `json:"max_ticks"`
}

type Plan struct {
	SchemaVersion uint32 `json:"schema_version"`
	ID            string `json:"plan_id"`
	Revision      uint32 `json:"revision"`
	Goal          string `json:"goal"`
	Nodes         []Node `json:"nodes"`
	Budget        Budget `json:"budget"`
}

type State struct {
	Completed      map[string]bool   `json:"completed,omitempty"`
	Skipped        map[string]bool   `json:"skipped,omitempty"`
	Failed         map[string]bool   `json:"failed,omitempty"`
	Attempts       map[string]uint32 `json:"attempts,omitempty"`
	Loops          map[string]uint32 `json:"loops,omitempty"`
	Branches       map[string]string `json:"branches,omitempty"`
	ActiveLoops    map[string]bool   `json:"active_loops,omitempty"`
	Steps          uint32            `json:"steps,omitempty"`
	RetiredSteps   uint32            `json:"retired_steps,omitempty"`
	WorldMutations uint32            `json:"world_mutations,omitempty"`
	Started        bool              `json:"started,omitempty"`
	StartedAt      uint64            `json:"started_at,omitempty"`
	Tick           uint64            `json:"tick,omitempty"`
}

// Validate checks only the bounded, engine-neutral plan shape. Capability
// existence, arguments, facts and postconditions remain Host responsibilities.
func (p Plan) Validate() error {
	if p.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported plan schema version %d", p.SchemaVersion)
	}
	if !identifier(p.ID) || p.Revision == 0 || !text(p.Goal, MaxConditionLen) {
		return fmt.Errorf("invalid plan identity")
	}
	if len(p.Nodes) == 0 || len(p.Nodes) > MaxNodes {
		return fmt.Errorf("plan nodes must be between 1 and %d", MaxNodes)
	}
	if p.Budget.MaxSteps == 0 || p.Budget.MaxSteps > 4096 ||
		p.Budget.MaxWorldMutations > 4096 || p.Budget.MaxTicks == 0 {
		return fmt.Errorf("invalid plan budget")
	}
	seen := make(map[string]struct{}, len(p.Nodes))
	controlled := make(map[string]string)
	for _, node := range p.Nodes {
		if !identifier(node.ID) || !validRisk(node.Risk) {
			return fmt.Errorf("invalid node %q", node.ID)
		}
		if node.Kind != Action && node.Kind != Branch && node.Kind != Loop {
			return fmt.Errorf("invalid node kind for %q", node.ID)
		}
		if _, exists := seen[node.ID]; exists {
			return fmt.Errorf("duplicate node %q", node.ID)
		}
		seen[node.ID] = struct{}{}
		if node.MaxAttempts == 0 || node.MaxAttempts > MaxAttempts {
			return fmt.Errorf("invalid attempts for %q", node.ID)
		}
		if node.MaxIterations > MaxLoopIterations {
			return fmt.Errorf("invalid loop bound for %q", node.ID)
		}
		if node.Priority < -1000 || node.Priority > 1000 {
			return fmt.Errorf("invalid priority for %q", node.ID)
		}
		if len(node.DependsOn) > MaxNodes || len(node.When) > MaxConditions ||
			len(node.Then) > MaxNodes || len(node.Else) > MaxNodes ||
			len(node.Children) > MaxNodes || !uniqueStrings(node.DependsOn) ||
			!uniqueStrings(node.When) || !uniqueStrings(node.Then) ||
			!uniqueStrings(node.Else) || !uniqueStrings(node.Children) {
			return fmt.Errorf("invalid references for %q", node.ID)
		}
		switch node.Kind {
		case Action:
			if !identifier(node.Capability) || len(node.When) != 0 ||
				len(node.Then) != 0 || len(node.Else) != 0 || len(node.Children) != 0 ||
				node.MaxIterations != 0 {
				return fmt.Errorf("invalid action node %q", node.ID)
			}
		case Branch:
			if node.Capability != "" || len(node.Then) == 0 || len(node.Else) == 0 ||
				len(node.Children) != 0 || node.MaxIterations != 0 ||
				node.WorldMutations != 0 || node.MaxAttempts != 1 {
				return fmt.Errorf("invalid branch node %q", node.ID)
			}
		case Loop:
			if node.Capability != "" || len(node.Then) != 0 || len(node.Else) != 0 ||
				len(node.Children) == 0 || node.MaxIterations == 0 ||
				node.WorldMutations != 0 || node.MaxAttempts != 1 {
				return fmt.Errorf("invalid loop node %q", node.ID)
			}
		}
		for _, condition := range node.When {
			if !text(condition, MaxConditionLen) {
				return fmt.Errorf("invalid condition in %q", node.ID)
			}
		}
		for _, dependency := range node.DependsOn {
			if !identifier(dependency) {
				return fmt.Errorf("invalid dependency in %q", node.ID)
			}
		}
		for _, child := range append(append(append([]string{}, node.Then...), node.Else...), node.Children...) {
			if owner, exists := controlled[child]; exists {
				return fmt.Errorf("node %q is controlled by both %q and %q", child, owner, node.ID)
			}
			controlled[child] = node.ID
		}
	}
	for _, node := range p.Nodes {
		for _, dependency := range append(append(append([]string{}, node.DependsOn...), node.Then...), node.Else...) {
			if !identifier(dependency) || dependency == node.ID || !containsNode(seen, dependency) {
				return fmt.Errorf("node %q references unknown node %q", node.ID, dependency)
			}
		}
		for _, child := range node.Children {
			if !containsNode(seen, child) {
				return fmt.Errorf("node %q references unknown child %q", node.ID, child)
			}
		}
	}
	parents := p.controlParents()
	for _, node := range p.Nodes {
		for _, dependency := range node.DependsOn {
			for current := node.ID; ; {
				parent, ok := parents[current]
				if !ok {
					break
				}
				if parent.kind == Loop && dependency == parent.id {
					return fmt.Errorf(
						"node %q depends on its unfinished loop %q",
						node.ID, parent.id)
				}
				current = parent.id
			}
		}
	}
	if hasCycle(p.Nodes) {
		return fmt.Errorf("plan graph contains a cycle")
	}
	if depth(p.Nodes) > MaxDepth {
		return fmt.Errorf("plan graph exceeds depth %d", MaxDepth)
	}
	return nil
}

// Ready returns deterministic action nodes whose dependencies and control path
// are satisfied. Call Advance first to resolve ready branches and bounded loops.
func (p Plan) Ready(state State, facts map[string]bool) []Node {
	if p.Validate() != nil || p.ValidateState(state) != nil {
		return nil
	}
	if stateFailed(state) {
		return nil
	}
	if state.Steps >= p.Budget.MaxSteps {
		return nil
	}
	parents := p.controlParents()
	ready := make([]Node, 0)
	for _, node := range p.Nodes {
		if node.Kind != Action || state.Completed[node.ID] || state.Skipped[node.ID] ||
			state.Attempts[node.ID] >= node.MaxAttempts ||
			!p.controlEnabled(state, node.ID, parents) {
			continue
		}
		ok := true
		for _, dependency := range node.DependsOn {
			if !state.Completed[dependency] && !state.Skipped[dependency] {
				ok = false
				break
			}
		}
		if ok {
			ready = append(ready, node)
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		if ready[i].Priority != ready[j].Priority {
			return ready[i].Priority > ready[j].Priority
		}
		return ready[i].ID < ready[j].ID
	})
	return ready
}

// Allows checks the host-owned execution budgets for one node application.
// The Host still owns capability arguments and postcondition validation.
func (p Plan) Allows(state State, nodeID string, tick uint64, worldMutations uint32) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := p.ValidateState(state); err != nil {
		return err
	}
	if stateFailed(state) {
		return fmt.Errorf("plan is already failed")
	}
	node, ok := p.node(nodeID)
	if !ok {
		return fmt.Errorf("unknown node %q", nodeID)
	}
	if node.Kind != Action {
		return fmt.Errorf("node %q is not an action", nodeID)
	}
	if state.Completed[nodeID] {
		return fmt.Errorf("node %q is already completed", nodeID)
	}
	if state.Skipped[nodeID] || !p.controlEnabled(state, nodeID, p.controlParents()) {
		return fmt.Errorf("node %q is not on the active control path", nodeID)
	}
	if state.Attempts[nodeID] >= node.MaxAttempts {
		return fmt.Errorf("node %q exceeded its attempt budget", nodeID)
	}
	if state.Steps >= p.Budget.MaxSteps {
		return fmt.Errorf("plan step budget exceeded")
	}
	if worldMutations > node.WorldMutations {
		return fmt.Errorf("node %q exceeded its declared mutation budget", nodeID)
	}
	if worldMutations > p.Budget.MaxWorldMutations-state.WorldMutations {
		return fmt.Errorf("plan world mutation budget exceeded")
	}
	if state.Started {
		if tick < state.Tick {
			return fmt.Errorf("plan tick moved backwards")
		}
		if tick-state.StartedAt > p.Budget.MaxTicks {
			return fmt.Errorf("plan tick budget exceeded")
		}
	}
	for _, dependency := range node.DependsOn {
		if !state.Completed[dependency] && !state.Skipped[dependency] {
			return fmt.Errorf("node %q is not ready", nodeID)
		}
	}
	return nil
}

// Apply records one verified node result and returns a new immutable-like State.
// It does not mutate the caller's maps.
func (p Plan) Apply(state State, nodeID string, tick uint64, worldMutations uint32) (State, error) {
	return p.recordAction(state, nodeID, tick, worldMutations, true)
}

func identifier(value string) bool {
	if len(value) == 0 || len(value) > 96 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			if index == 0 && (char < 'a' || char > 'z') {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func text(value string, max int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= max
}

func uniqueStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validRisk(value string) bool {
	switch value {
	case "low", "moderate", "high", "critical":
		return true
	default:
		return false
	}
}

func containsNode(seen map[string]struct{}, value string) bool {
	_, ok := seen[value]
	return ok
}

func hasCycle(nodes []Node) bool {
	graph := graphEdges(nodes)
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, next := range graph[id] {
			if next != "" && visit(next) {
				return true
			}
		}
		delete(visiting, id)
		visited[id] = true
		return false
	}
	for _, node := range nodes {
		if visit(node.ID) {
			return true
		}
	}
	return false
}

func depth(nodes []Node) int {
	graph := graphEdges(nodes)
	cache := make(map[string]int)
	var visit func(string) int
	visit = func(id string) int {
		if value, ok := cache[id]; ok {
			return value
		}
		best := 1
		for _, next := range graph[id] {
			if value := 1 + visit(next); value > best {
				best = value
			}
		}
		cache[id] = best
		return best
	}
	best := 0
	for _, node := range nodes {
		if value := visit(node.ID); value > best {
			best = value
		}
	}
	return best
}

func graphEdges(nodes []Node) map[string][]string {
	result := make(map[string][]string, len(nodes))
	for _, node := range nodes {
		result[node.ID] = nil
	}
	for _, node := range nodes {
		for _, dependency := range node.DependsOn {
			if dependency != "" {
				result[dependency] = append(result[dependency], node.ID)
			}
		}
		result[node.ID] = append(result[node.ID], node.Then...)
		result[node.ID] = append(result[node.ID], node.Else...)
		result[node.ID] = append(result[node.ID], node.Children...)
	}
	return result
}

func (p Plan) node(id string) (Node, bool) {
	for _, node := range p.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return Node{}, false
}

func cloneBoolMap(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneUintMap(source map[string]uint32) map[string]uint32 {
	result := make(map[string]uint32, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
