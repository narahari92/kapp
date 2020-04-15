package diffgraph

import (
	"fmt"

	ctlconf "github.com/k14s/kapp/pkg/kapp/config"
)

type ChangeGraph struct {
	changes []*Change
}

func NewChangeGraph(changes []ActualChange,
	additionalChangeGroups []ctlconf.AdditionalChangeGroup,
	additionalChangeRules []ctlconf.AdditionalChangeRule) (*ChangeGraph, error) {

	graphChanges := []*Change{}

	for _, change := range changes {
		graphChanges = append(graphChanges, &Change{
			Change:                 change,
			additionalChangeGroups: additionalChangeGroups,
			additionalChangeRules:  additionalChangeRules,
			requiredWaitingFor:     map[*Change]struct{}{},
		})
	}

	for _, graphChange := range graphChanges {
		rules, err := graphChange.ApplicableRules()
		if err != nil {
			return nil, err
		}

		for _, rule := range rules {
			switch {
			case rule.Order == ChangeRuleOrderAfter:
				matchedChanges, err := Changes(graphChanges).MatchesRule(rule, graphChange)
				if err != nil {
					return nil, err
				}
				graphChange.WaitingFor = append(graphChange.WaitingFor, matchedChanges...)
				if !rule.IgnoreIfCyclical {
					for _, matchedChange := range matchedChanges {
						graphChange.requiredWaitingFor[matchedChange] = struct{}{}
					}
				}

			case rule.Order == ChangeRuleOrderBefore:
				matchedChanges, err := Changes(graphChanges).MatchesRule(rule, graphChange)
				if err != nil {
					return nil, err
				}
				for _, matchedChange := range matchedChanges {
					matchedChange.WaitingFor = append(matchedChange.WaitingFor, graphChange)
					if !rule.IgnoreIfCyclical {
						matchedChange.requiredWaitingFor[graphChange] = struct{}{}
					}
				}
			}
		}
	}

	graph := &ChangeGraph{graphChanges}
	graph.pruneNilsAndDedup()

	err := graph.checkCycles()
	if err != nil {
		return nil, err
	}

	graph.pruneNilsAndDedup()

	return graph, nil
}

func (g *ChangeGraph) All() []*Change {
	return g.AllMatching(func(_ *Change) bool { return true })
}

func (g *ChangeGraph) AllMatching(matchFunc func(*Change) bool) []*Change {
	var result []*Change
	// Need to do this _only_ at the first level since
	// all changes are included at the top level
	for _, change := range g.changes {
		if matchFunc(change) {
			result = append(result, change)
		}
	}
	return result
}

func (g *ChangeGraph) RemoveMatching(matchFunc func(*Change) bool) {
	var result []*Change
	// Need to do this _only_ at the first level since
	// all changes are included at the top level
	for _, change := range g.changes {
		if !matchFunc(change) {
			result = append(result, change)
		}
	}
	g.changes = result
}

func (g *ChangeGraph) Print() {
	fmt.Printf("%s", g.PrintStr())
}

func (g *ChangeGraph) PrintStr() string {
	return g.printChanges(g.changes, map[*Change]bool{}, "")
}

func (g *ChangeGraph) printChanges(changes []*Change, visitedChanges map[*Change]bool, indent string) string {
	var result string

	for _, change := range changes {
		result += fmt.Sprintf("%s(%s) %s\n", indent, change.Change.Op(), change.Change.Resource().Description())

		if _, found := visitedChanges[change]; !found {
			visitedChanges[change] = true
			result += g.printChanges(change.WaitingFor, visitedChanges, indent+"  ")
			delete(visitedChanges, change)
		} else {
			result += indent + "cycle found\n"
		}
	}

	return result
}

func (g *ChangeGraph) pruneNilsAndDedup() {
	for _, change := range g.changes {
		seenWaitingFor := map[*Change]struct{}{}
		newWaitingFor := []*Change{}

		for _, change := range change.WaitingFor {
			if change != nil {
				if _, ok := seenWaitingFor[change]; !ok {
					seenWaitingFor[change] = struct{}{}
					newWaitingFor = append(newWaitingFor, change)
				}
			}
		}

		change.WaitingFor = newWaitingFor
	}
}

func (g *ChangeGraph) checkCycles() error {
	for _, change := range g.changes {
		changeDesc := fmt.Sprintf("[%s]", change.Change.Resource().Description())

		err := g.checkCyclesInChanges(change.WaitingFor,
			change.requiredWaitingFor, map[*Change]bool{change: true}, changeDesc)
		if err != nil {
			return err
		}
	}

	return nil
}

func (g *ChangeGraph) checkCyclesInChanges(changes []*Change, requiredChanges map[*Change]struct{},
	visitedChanges map[*Change]bool, descPath string) error {

	for i, change := range changes {
		if change == nil {
			continue // optional changes get pruned out
		}

		changeDesc := fmt.Sprintf("%s -> [%s]", descPath, change.Change.Resource().Description())

		if _, found := visitedChanges[change]; found {
			if _, found := requiredChanges[change]; found {
				return fmt.Errorf("Detected cycle while ordering changes: %s (found repeated: %s)",
					changeDesc, change.Change.Resource().Description())
			}
			changes[i] = nil // prune out optional change
		}

		if changes[i] != nil {
			visitedChanges[change] = true

			err := g.checkCyclesInChanges(change.WaitingFor,
				change.requiredWaitingFor, visitedChanges, changeDesc)
			if err != nil {
				if _, found := requiredChanges[change]; found {
					return err
				}
				changes[i] = nil // prune out optional change
			}

			delete(visitedChanges, change)
		}
	}

	return nil
}
