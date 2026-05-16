package writetarget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	yamlv3 "go.yaml.in/yaml/v3"
)

// UpdateStructuredField updates a dot-path field in a JSON or YAML file.
func UpdateStructuredField(path, field, value string) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return UpdateJSONField(path, field, value)
	case ".yaml", ".yml":
		return UpdateYAMLField(path, field, value)
	default:
		return fmt.Errorf("unsupported file extension for %s", path)
	}
}

// UpdateJSONField updates a dot-path field in a JSON document.
func UpdateJSONField(path, field, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return fmt.Errorf("parse JSON %s: %w", path, err)
	}
	if err := setStructuredValue(&doc, parseFieldPath(field), value); err != nil {
		return fmt.Errorf("set %s:%s: %w", path, field, err)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON %s: %w", path, err)
	}
	out = append(out, '\n')
	return os.WriteFile(path, out, 0644)
}

// UpdateYAMLField updates a dot-path field in a YAML document.
func UpdateYAMLField(path, field, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse YAML %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("YAML %s is empty", path)
	}
	if err := setYAMLValue(doc.Content[0], parseFieldPath(field), value); err != nil {
		return fmt.Errorf("set %s:%s: %w", path, field, err)
	}
	out, err := yamlv3.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshal YAML %s: %w", path, err)
	}
	return os.WriteFile(path, out, 0644)
}

// UpdateKustomizeImage updates or appends an image entry in kustomization.yaml.
func UpdateKustomizeImage(path, imageName, newName, newTag string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse YAML %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("YAML %s is empty", path)
	}
	root := doc.Content[0]
	if root.Kind != yamlv3.MappingNode {
		return fmt.Errorf("expected kustomization mapping")
	}
	images := yamlMappingValue(root, "images")
	if images == nil {
		images = &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content,
			&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: "images"},
			images,
		)
	}
	if images.Kind != yamlv3.SequenceNode {
		return fmt.Errorf("images is not a sequence")
	}
	for _, image := range images.Content {
		if image.Kind != yamlv3.MappingNode {
			continue
		}
		if value := yamlMappingValue(image, "name"); value != nil && value.Value == imageName {
			setYAMLMappingScalar(image, "newName", firstNonEmpty(newName, imageName))
			setYAMLMappingScalar(image, "newTag", newTag)
			out, err := yamlv3.Marshal(&doc)
			if err != nil {
				return fmt.Errorf("marshal YAML %s: %w", path, err)
			}
			return os.WriteFile(path, out, 0644)
		}
	}
	image := &yamlv3.Node{Kind: yamlv3.MappingNode, Tag: "!!map"}
	setYAMLMappingScalar(image, "name", imageName)
	setYAMLMappingScalar(image, "newName", firstNonEmpty(newName, imageName))
	setYAMLMappingScalar(image, "newTag", newTag)
	images.Content = append(images.Content, image)
	out, err := yamlv3.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshal YAML %s: %w", path, err)
	}
	return os.WriteFile(path, out, 0644)
}

type fieldToken struct {
	Name  string
	Index *int
}

func parseFieldPath(field string) []fieldToken {
	parts := strings.Split(field, ".")
	tokens := make([]fieldToken, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		token := fieldToken{Name: part}
		if start := strings.Index(part, "["); start >= 0 && strings.HasSuffix(part, "]") {
			token.Name = part[:start]
			rawIndex := strings.TrimSuffix(part[start+1:], "]")
			if idx, err := strconv.Atoi(rawIndex); err == nil {
				token.Index = &idx
			}
		}
		tokens = append(tokens, token)
	}
	return tokens
}

func setStructuredValue(cur *any, tokens []fieldToken, value string) error {
	if len(tokens) == 0 {
		*cur = value
		return nil
	}
	token := tokens[0]
	m, ok := (*cur).(map[string]any)
	if !ok {
		return fmt.Errorf("expected object at %q", token.Name)
	}
	next, ok := m[token.Name]
	if !ok {
		if len(tokens) == 1 {
			m[token.Name] = value
			return nil
		}
		next = map[string]any{}
		m[token.Name] = next
	}
	if token.Index != nil {
		items, ok := next.([]any)
		if !ok {
			return fmt.Errorf("expected array at %q", token.Name)
		}
		if *token.Index < 0 || *token.Index >= len(items) {
			return fmt.Errorf("index %d out of range at %q", *token.Index, token.Name)
		}
		if len(tokens) == 1 {
			items[*token.Index] = value
			m[token.Name] = items
			return nil
		}
		child := items[*token.Index]
		if err := setStructuredValue(&child, tokens[1:], value); err != nil {
			return err
		}
		items[*token.Index] = child
		m[token.Name] = items
		return nil
	}
	if len(tokens) == 1 {
		m[token.Name] = value
		return nil
	}
	if err := setStructuredValue(&next, tokens[1:], value); err != nil {
		return err
	}
	m[token.Name] = next
	return nil
}

func setYAMLValue(cur *yamlv3.Node, tokens []fieldToken, value string) error {
	if len(tokens) == 0 {
		cur.Kind = yamlv3.ScalarNode
		cur.Tag = "!!str"
		cur.Value = value
		return nil
	}
	if cur.Kind != yamlv3.MappingNode {
		return fmt.Errorf("expected mapping at %q", tokens[0].Name)
	}
	valueNode := yamlMappingValue(cur, tokens[0].Name)
	if valueNode == nil {
		valueNode = &yamlv3.Node{Kind: yamlv3.MappingNode, Tag: "!!map"}
		cur.Content = append(cur.Content,
			&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: tokens[0].Name},
			valueNode,
		)
	}
	if tokens[0].Index != nil {
		if valueNode.Kind != yamlv3.SequenceNode {
			return fmt.Errorf("expected sequence at %q", tokens[0].Name)
		}
		idx := *tokens[0].Index
		if idx < 0 || idx >= len(valueNode.Content) {
			return fmt.Errorf("index %d out of range at %q", idx, tokens[0].Name)
		}
		if len(tokens) == 1 {
			valueNode.Content[idx] = &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: value}
			return nil
		}
		return setYAMLValue(valueNode.Content[idx], tokens[1:], value)
	}
	if len(tokens) == 1 {
		valueNode.Kind = yamlv3.ScalarNode
		valueNode.Tag = "!!str"
		valueNode.Value = value
		valueNode.Content = nil
		return nil
	}
	return setYAMLValue(valueNode, tokens[1:], value)
}

func yamlMappingValue(node *yamlv3.Node, key string) *yamlv3.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func setYAMLMappingScalar(node *yamlv3.Node, key, value string) {
	existing := yamlMappingValue(node, key)
	if existing == nil {
		node.Content = append(node.Content,
			&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: key},
			&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: value},
		)
		return
	}
	existing.Kind = yamlv3.ScalarNode
	existing.Tag = "!!str"
	existing.Value = value
	existing.Content = nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
