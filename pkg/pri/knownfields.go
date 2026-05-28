package pri

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

type fieldRule struct {
	fields   map[string]fieldRule
	item     *fieldRule
	freeForm bool
}

func object(fields map[string]fieldRule) fieldRule {
	return fieldRule{fields: fields}
}

func list(item fieldRule) fieldRule {
	return fieldRule{item: &item}
}

func freeForm() fieldRule {
	return fieldRule{freeForm: true}
}

var (
	scalarRule    = fieldRule{}
	stringMapRule = freeForm()
	metadataRule  = freeForm()

	promotionRule = object(map[string]fieldRule{
		"apiVersion": scalarRule,
		"kind":       scalarRule,
		"metadata":   metadataRule,
		"spec": object(map[string]fieldRule{
			"unit": scalarRule,
			"artifacts": list(object(map[string]fieldRule{
				"name":    scalarRule,
				"version": scalarRule,
				"digest":  scalarRule,
				"uri":     scalarRule,
			})),
			"plan": object(map[string]fieldRule{
				"ref": scalarRule,
			}),
			"checks": list(object(map[string]fieldRule{
				"name":         scalarRule,
				"required":     scalarRule,
				"policyRef":    scalarRule,
				"evidenceRefs": list(scalarRule),
			})),
			"targets": list(object(map[string]fieldRule{
				"name":   scalarRule,
				"labels": stringMapRule,
				"delivery": object(map[string]fieldRule{
					"ref":        scalarRule,
					"mode":       scalarRule,
					"parameters": stringMapRule,
				}),
			})),
			"evidence": list(object(map[string]fieldRule{
				"name":   scalarRule,
				"type":   scalarRule,
				"uri":    scalarRule,
				"digest": scalarRule,
			})),
		}),
	})

	promotionRunRule = object(map[string]fieldRule{
		"apiVersion": scalarRule,
		"kind":       scalarRule,
		"metadata":   metadataRule,
		"spec": object(map[string]fieldRule{
			"promotionRef": scalarRule,
		}),
		"status": object(map[string]fieldRule{
			"phase":               scalarRule,
			"implementationPhase": scalarRule,
			"startedAt":           scalarRule,
			"completedAt":         scalarRule,
			"attempts": list(object(map[string]fieldRule{
				"id":                  scalarRule,
				"phase":               scalarRule,
				"implementationPhase": scalarRule,
				"startedAt":           scalarRule,
				"completedAt":         scalarRule,
			})),
			"checkResults": list(object(map[string]fieldRule{
				"check":               scalarRule,
				"phase":               scalarRule,
				"implementationPhase": scalarRule,
				"evidenceRefs":        list(scalarRule),
			})),
			"targetResults": list(object(map[string]fieldRule{
				"target":              scalarRule,
				"phase":               scalarRule,
				"implementationPhase": scalarRule,
				"evidenceRefs":        list(scalarRule),
			})),
		}),
	})

	evidenceRule = object(map[string]fieldRule{
		"apiVersion": scalarRule,
		"kind":       scalarRule,
		"metadata":   metadataRule,
		"spec": object(map[string]fieldRule{
			"type":        scalarRule,
			"uri":         scalarRule,
			"digest":      scalarRule,
			"subjectRefs": list(scalarRule),
		}),
	})

	bindingRule = object(map[string]fieldRule{
		"apiVersion": scalarRule,
		"kind":       scalarRule,
		"metadata":   metadataRule,
		"spec": object(map[string]fieldRule{
			"category":      scalarRule,
			"summary":       scalarRule,
			"priVersions":   list(scalarRule),
			"adoptionModes": list(scalarRule),
			"roundTrip":     scalarRule,
			"mappings": object(map[string]fieldRule{
				"objects": list(mappingRule()),
				"fields":  list(mappingRule()),
			}),
			"requiredConfiguration": list(object(map[string]fieldRule{
				"name":        scalarRule,
				"description": scalarRule,
			})),
			"unsupported": list(scalarRule),
			"references": list(object(map[string]fieldRule{
				"title": scalarRule,
				"uri":   scalarRule,
			})),
		}),
	})

	conformanceProfileRule = object(map[string]fieldRule{
		"apiVersion": scalarRule,
		"kind":       scalarRule,
		"metadata":   metadataRule,
		"spec": object(map[string]fieldRule{
			"priVersion":   scalarRule,
			"adoptionMode": scalarRule,
			"conformance": object(map[string]fieldRule{
				"document": scalarRule,
				"runtime":  scalarRule,
				"decision": scalarRule,
			}),
		}),
	})
)

func mappingRule() fieldRule {
	return object(map[string]fieldRule{
		"pri":      scalarRule,
		"external": scalarRule,
		"notes":    scalarRule,
	})
}

func checkKnownFields(kind string, node *yaml.Node) error {
	var rule fieldRule
	switch kind {
	case KindPromotion:
		rule = promotionRule
	case KindPromotionRun:
		rule = promotionRunRule
	case KindEvidence:
		rule = evidenceRule
	case KindBinding:
		rule = bindingRule
	case KindConformanceProfile:
		rule = conformanceProfileRule
	default:
		return nil
	}
	return checkNode("$", node, rule)
}

func checkNode(path string, node *yaml.Node, rule fieldRule) error {
	if node == nil || rule.freeForm {
		return nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return checkNode(path, node.Content[0], rule)
	}
	if rule.item != nil {
		if node.Kind != yaml.SequenceNode {
			return nil
		}
		for i, item := range node.Content {
			if err := checkNode(fmt.Sprintf("%s[%d]", path, i), item, *rule.item); err != nil {
				return err
			}
		}
		return nil
	}
	if len(rule.fields) == 0 {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valueNode := node.Content[i+1]
		childRule, ok := rule.fields[keyNode.Value]
		if !ok {
			return fmt.Errorf("%s.%s is not a PRI v0.1 field", path, keyNode.Value)
		}
		childPath := path + "." + keyNode.Value
		if path == "$" {
			childPath = "$." + keyNode.Value
		}
		if err := checkNode(childPath, valueNode, childRule); err != nil {
			return err
		}
	}
	return nil
}
