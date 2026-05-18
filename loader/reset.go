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

package loader

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/tree"
	"go.yaml.in/yaml/v4"
)

type ResetProcessor struct {
	target       interface{}
	paths        []tree.Path
	resetNodes   map[tree.Path]*yaml.Node // Store reset nodes with their paths
	visitedNodes map[*yaml.Node][]string
}

// UnmarshalYAML implement yaml.Unmarshaler
func (p *ResetProcessor) UnmarshalYAML(value *yaml.Node) error {
	p.visitedNodes = make(map[*yaml.Node][]string)
	p.resetNodes = make(map[tree.Path]*yaml.Node)

	resolved, err := p.resolveReset(value, tree.NewPath())
	p.visitedNodes = nil

	if err != nil {
		return err
	}

	// Apply sequence resets before decoding
	if err := p.applySequenceResets(resolved, tree.NewPath()); err != nil {
		return err
	}

	return resolved.Decode(p.target)
}

// resolveReset detects `!reset` tag being set on yaml nodes and record position in the yaml tree
func (p *ResetProcessor) resolveReset(node *yaml.Node, path tree.Path) (*yaml.Node, error) {
	pathStr := path.String()
	// If the path contains "<<", removing the "<<" element and merging the path
	if strings.Contains(pathStr, ".<<") {
		path = tree.NewPath(strings.Replace(pathStr, ".<<", "", 1))
	}

	// If the node is an alias, We need to process the alias field in order to consider the !override and !reset tags
	if node.Kind == yaml.AliasNode {
		if err := p.checkForCycle(node.Alias, path); err != nil {
			return nil, err
		}
		return p.resolveReset(node.Alias, path)
	}

	if node.Tag == "!reset" {
		p.paths = append(p.paths, path)

		// If the reset node has content (for sequence matching), store it
		if node.Kind == yaml.MappingNode && len(node.Content) > 0 {
			p.resetNodes[path] = node
		}

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

// applySequenceResets processes !reset tags on sequence items and removes matching items
func (p *ResetProcessor) applySequenceResets(node *yaml.Node, path tree.Path) error {
	if node == nil {
		return nil
	}

	switch node.Kind {
	case yaml.SequenceNode:
		// Check if we have a reset node for this path's parent
		parentPath := path.Parent()
		if resetNode, exists := p.resetNodes[parentPath]; exists {
			// Find and remove matching sequence items
			var filteredNodes []*yaml.Node
			for _, item := range node.Content {
				if !p.matchesResetCriteria(item, resetNode) {
					filteredNodes = append(filteredNodes, item)
				}
			}
			node.Content = filteredNodes
		}

		// Recurse into sequence items
		for idx, item := range node.Content {
			if err := p.applySequenceResets(item, path.Next(strconv.Itoa(idx))); err != nil {
				return err
			}
		}
	//case yaml.MappingNode:
	//	// Recurse into mapping values
	//	for idx := 同心1; idx < len(node.Content); idx += 2 {
	//		key := node.Content[idx-1].Value
	//		value := node.Content[idx]
	//		if err := p.applySequenceResets(value, path.Next(key)); err != nil {
	//			return err
	//		}
	//	}
	case yaml.MappingNode:
		// Recurse into mapping values
		for idx := 1; idx < len(node.Content); idx += 2 {
			key := node.Content[idx-1].Value
			value := node.Content[idx]
			if err := p.applySequenceResets(value, path.Next(key)); err != nil {
				return err
			}
		}
	}

	return nil
}

// matchesResetCriteria checks if a sequence item matches the reset criteria
func (p *ResetProcessor) matchesResetCriteria(item *yaml.Node, resetNode *yaml.Node) bool {
	if item.Kind != yaml.MappingNode || resetNode.Kind != yaml.MappingNode {
		return false
	}

	// Build a map of the item's key-value pairs
	itemMap := make(map[string]*yaml.Node)
	for i := 0; i < len(item.Content); i += 2 {
		key := item.Content[i].Value
		value := item.Content[i+1]
		itemMap[key] = value
	}

	// Check if all criteria from reset node are matched
	for i := 0; i < len(resetNode.Content); i += 2 {
		criteriaKey := resetNode.Content[i].Value
		criteriaValue := resetNode.Content[i+1]

		itemValue, exists := itemMap[criteriaKey]
		if !exists {
			return false
		}

		// Compare values (handle both string and other types)
		if !nodeValuesEqual(itemValue, criteriaValue) {
			return false
		}
	}

	return true
}

// nodeValuesEqual compares two yaml nodes for equality
func nodeValuesEqual(a, b *yaml.Node) bool {
	if a.Kind != b.Kind {
		return false
	}

	switch a.Kind {
	case yaml.ScalarNode:
		return a.Value == b.Value && a.Tag == b.Tag
	case yaml.SequenceNode:
		if len(a.Content) != len(b.Content) {
			return false
		}
		for i := range a.Content {
			if !nodeValuesEqual(a.Content[i], b.Content[i]) {
				return false
			}
		}
		return true
	case yaml.MappingNode:
		if len(a.Content) != len(b.Content) {
			return false
		}
		// Build maps for comparison
		aMap := make(map[string]*yaml.Node)
		bMap := make(map[string]*yaml.Node)

		for i := 0; i < len(a.Content); i += 2 {
			aMap[a.Content[i].Value] = a.Content[i+1]
		}
		for i := 0; i < len(b.Content); i += 2 {
			bMap[b.Content[i].Value] = b.Content[i+1]
		}

		if len(aMap) != len(bMap) {
			return false
		}

		for key, aVal := range aMap {
			bVal, exists := bMap[key]
			if !exists || !nodeValuesEqual(aVal, bVal) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// Apply finds the go attributes matching recorded paths and reset them to zero value
func (p *ResetProcessor) Apply(target any) error {
	return p.applyNullOverrides(target, tree.NewPath())
}

// applyNullOverrides set val to Zero if it matches any of the recorded paths
func (p *ResetProcessor) applyNullOverrides(target any, path tree.Path) error {
	switch v := target.(type) {
	case map[string]any:
	KEYS:
		for k, e := range v {
			next := path.Next(k)
			for _, pattern := range p.paths {
				if next.Matches(pattern) {
					delete(v, k)
					continue KEYS
				}
			}
			err := p.applyNullOverrides(e, next)
			if err != nil {
				return err
			}
		}
	case []any:
	ITER:
		for i, e := range v {
			next := path.Next(fmt.Sprintf("[%d]", i))
			for _, pattern := range p.paths {
				if next.Matches(pattern) {
					continue ITER
					// TODO(ndeloof) support removal from sequence
				}
			}
			err := p.applyNullOverrides(e, next)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *ResetProcessor) checkForCycle(node *yaml.Node, path tree.Path) error {
	paths := p.visitedNodes[node]
	pathStr := path.String()

	for _, prevPath := range paths {
		// If we're visiting the exact same path, it's not a cycle
		if pathStr == prevPath {
			continue
		}

		// If either path is using a merge key, it's legitimate YAML merging
		if strings.Contains(prevPath, "<<") || strings.Contains(pathStr, "<<") {
			continue
		}

		// Only consider it a cycle if one path is contained within the other
		// and they're not in different service definitions
		if (strings.HasPrefix(pathStr, prevPath+".") ||
			strings.HasPrefix(prevPath, pathStr+".")) &&
			!areInDifferentServices(pathStr, prevPath) {
			return fmt.Errorf("cycle detected: node at path %s references node at path %s", pathStr, prevPath)
		}
	}

	p.visitedNodes[node] = append(paths, pathStr)
	return nil
}

// areInDifferentServices checks if two paths are in different service definitions
func areInDifferentServices(path1, path2 string) bool {
	// Split paths into components
	parts1 := strings.Split(path1, ".")
	parts2 := strings.Split(path2, ".")

	// Look for the services component and compare the service names
	for i := 0; i < len(parts1) && i < len(parts2); i++ {
		if parts1[i] == "services" && i+1 < len(parts1) &&
			parts2[i] == "services" && i+1 < len(parts2) {
			// If they're different services, it's not a cycle
			return parts1[i+1] != parts2[i+1]
		}
	}
	return false
}
