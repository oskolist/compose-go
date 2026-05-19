/*
   Copyright 2020 The Compose Specification Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

// reset.go
package loader

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/oskolist/compose-go/v2/tree"
	"go.yaml.in/yaml/v4"
)

// sequenceResetCriteria holds field-value pairs that must ALL match for a
// sequence item to be removed. An empty fields map matches nothing.
type sequenceResetCriteria struct {
	fields map[string]string
}

// matchesAllFields returns true when item (a map[string]any) contains every
// key→value pair listed in the criteria.
func (c sequenceResetCriteria) matchesAllFields(item any) bool {
	if len(c.fields) == 0 {
		return false // empty criteria never remove anything
	}
	m, ok := item.(map[string]any)
	if !ok {
		// For scalar sequences (e.g. environment: [FOO=bar]) the item is a
		// string; try to match "key=value" notation.
		s, ok2 := item.(string)
		if !ok2 {
			return false
		}
		for k, v := range c.fields {
			if s != k+"="+v && s != k+": "+v {
				return false
			}
		}
		return true
	}
	for k, want := range c.fields {
		got, exists := m[k]
		if !exists {
			return false
		}
		// Compare as strings to stay type-agnostic (YAML scalars decode to
		// string/int/bool; comparing via fmt.Sprint is a pragmatic choice).
		if fmt.Sprint(got) != want {
			return false
		}
	}
	return true
}

// matchesAnySequenceCriteria returns true when item satisfies at least one
// of the provided criteria sets.
func matchesAnySequenceCriteria(item any, criteriaList []sequenceResetCriteria) bool {
	for _, c := range criteriaList {
		if c.matchesAllFields(item) {
			return true
		}
	}
	return false
}

type ResetProcessor struct {
	target         interface{}
	paths          []tree.Path
	sequenceResets map[tree.Path][]sequenceResetCriteria
	visitedNodes   map[*yaml.Node][]string
}

// UnmarshalYAML implement yaml.Unmarshaler
func (p *ResetProcessor) UnmarshalYAML(value *yaml.Node) error {
	p.visitedNodes = make(map[*yaml.Node][]string)
	if p.sequenceResets == nil {
		p.sequenceResets = make(map[tree.Path][]sequenceResetCriteria)
	}
	resolved, err := p.resolveReset(value, tree.NewPath())
	p.visitedNodes = nil
	if err != nil {
		return err
	}
	return resolved.Decode(p.target)
}

// resolveReset detects `!reset` / `!override` tags and records positions.
func (p *ResetProcessor) resolveReset(node *yaml.Node, path tree.Path) (*yaml.Node, error) {
	pathStr := path.String()
	if strings.Contains(pathStr, ".<<") {
		path = tree.NewPath(strings.Replace(pathStr, ".<<", "", 1))
	}

	if node.Kind == yaml.AliasNode {
		if err := p.checkForCycle(node.Alias, path); err != nil {
			return nil, err
		}
		return p.resolveReset(node.Alias, path)
	}

	if node.Tag == "!reset" {
		p.paths = append(p.paths, path)
		return nil, nil
	}
	if node.Tag == "!override" {
		p.paths = append(p.paths, path)
		return node, nil
	}

	keys := map[string]int{}
	switch node.Kind {
	case yaml.SequenceNode:
		var nodes []*yaml.Node
		for idx, v := range node.Content {
			if v.Tag == "!reset" {
				// Sequence-item reset: collect match criteria, drop from override.
				criteria, err := extractMappingCriteria(v)
				if err != nil {
					return nil, fmt.Errorf("line %d: invalid !reset sequence item: %w", v.Line, err)
				}
				if p.sequenceResets == nil {
					p.sequenceResets = make(map[tree.Path][]sequenceResetCriteria)
				}
				p.sequenceResets[path] = append(p.sequenceResets[path], criteria)
				continue // don't include in override output
			}
			next := path.Next(strconv.Itoa(idx))
			resolved, err := p.resolveReset(v, next)
			if err != nil {
				return nil, err
			}
			if resolved != nil {
				nodes = append(nodes, resolved)
			}
		}
		node.Content = nodes

	case yaml.MappingNode:
		var key string
		var nodes []*yaml.Node
		for idx, v := range node.Content {
			if idx%2 == 0 {
				key = v.Value
				if line, seen := keys[key]; seen {
					return nil, fmt.Errorf("line %d: mapping key %#v already defined at line %d", v.Line, key, line)
				}
				keys[key] = v.Line
			} else {
				resolved, err := p.resolveReset(v, path.Next(key))
				if err != nil {
					return nil, err
				}
				if resolved != nil {
					nodes = append(nodes, node.Content[idx-1], resolved)
				}
			}
		}
		node.Content = nodes
	}
	return node, nil
}

// extractMappingCriteria reads key→value pairs from a !reset-tagged YAML node.
// The node is the value node that follows "- !reset" in a sequence.
func extractMappingCriteria(node *yaml.Node) (sequenceResetCriteria, error) {
	criteria := sequenceResetCriteria{fields: make(map[string]string)}

	// The YAML parser attaches the tag to the node itself.  When the user writes
	//
	//   - !reset
	//     name: ssh
	//
	// the parser produces a MappingNode with tag "!reset".
	// When the user writes
	//
	//   - !reset
	//
	// with no children it may be a ScalarNode (null).

	var mappingNode *yaml.Node
	switch node.Kind {
	case yaml.MappingNode:
		mappingNode = node
	case yaml.ScalarNode:
		// No fields — return empty criteria (no-op at item level).
		return criteria, nil
	case yaml.SequenceNode:
		// Unusual, but handle gracefully.
		return criteria, nil
	}

	if mappingNode == nil {
		return criteria, nil
	}

	for i := 0; i+1 < len(mappingNode.Content); i += 2 {
		keyNode := mappingNode.Content[i]
		valNode := mappingNode.Content[i+1]
		if keyNode.Kind != yaml.ScalarNode {
			return criteria, fmt.Errorf("non-scalar key in !reset criteria at line %d", keyNode.Line)
		}
		if valNode.Kind != yaml.ScalarNode {
			return criteria, fmt.Errorf(
				"non-scalar value for key %q in !reset criteria at line %d (only scalar values supported)",
				keyNode.Value, valNode.Line,
			)
		}
		criteria.fields[keyNode.Value] = valNode.Value
	}
	return criteria, nil
}

// Apply walks target and removes entries that match recorded reset paths/criteria.
func (p *ResetProcessor) Apply(target any) error {
	_, err := p.applyNullOverrides(target, tree.NewPath())
	return err
}

// applyNullOverrides returns the (possibly replaced) value for target.
// Returning the value lets the caller swap out a slice when items are removed.
func (p *ResetProcessor) applyNullOverrides(target any, path tree.Path) (any, error) {
	switch v := target.(type) {
	case map[string]any:
		for k, e := range v {
			next := path.Next(k)
			// Check scalar-path resets first.
			deleted := false
			for _, pattern := range p.paths {
				if next.Matches(pattern) {
					delete(v, k)
					deleted = true
					break
				}
			}
			if deleted {
				continue
			}
			replaced, err := p.applyNullOverrides(e, next)
			if err != nil {
				return nil, err
			}
			v[k] = replaced
		}
		return v, nil

	case []any:
		// 1. Apply sequence-item reset criteria for this path.
		if criteriaList, ok := p.sequenceResets[path]; ok && len(criteriaList) > 0 {
			filtered := make([]any, 0, len(v))
			for _, item := range v {
				if !matchesAnySequenceCriteria(item, criteriaList) {
					filtered = append(filtered, item)
				}
			}
			v = filtered
		}

		// 2. Recurse into remaining items and apply path-based resets.
		result := make([]any, 0, len(v))
		for i, e := range v {
			next := path.Next(fmt.Sprintf("[%d]", i))
			skip := false
			for _, pattern := range p.paths {
				if next.Matches(pattern) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
			replaced, err := p.applyNullOverrides(e, next)
			if err != nil {
				return nil, err
			}
			result = append(result, replaced)
		}
		return result, nil
	}

	return target, nil
}

func (p *ResetProcessor) checkForCycle(node *yaml.Node, path tree.Path) error {
	paths := p.visitedNodes[node]
	pathStr := path.String()

	for _, prevPath := range paths {
		if pathStr == prevPath {
			continue
		}
		if strings.Contains(prevPath, "<<") || strings.Contains(pathStr, "<<") {
			continue
		}
		if (strings.HasPrefix(pathStr, prevPath+".") ||
			strings.HasPrefix(prevPath, pathStr+".")) &&
			!areInDifferentServices(pathStr, prevPath) {
			return fmt.Errorf("cycle detected: node at path %s references node at path %s", pathStr, prevPath)
		}
	}

	p.visitedNodes[node] = append(paths, pathStr)
	return nil
}

func areInDifferentServices(path1, path2 string) bool {
	parts1 := strings.Split(path1, ".")
	parts2 := strings.Split(path2, ".")

	for i := 0; i < len(parts1) && i < len(parts2); i++ {
		if parts1[i] == "services" && i+1 < len(parts1) &&
			parts2[i] == "services" && i+1 < len(parts2) {
			return parts1[i+1] != parts2[i+1]
		}
	}
	return false
}
