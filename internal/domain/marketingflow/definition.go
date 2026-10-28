package marketingflow

import (
	"regexp"
	"strconv"
	"strings"
)

// Definition is the bounded authoring contract. It cannot execute scripts or URLs.
type Definition struct {
	SchemaVersion int    `json:"schema_version"`
	EventSource   string `json:"event_source"`
	Entry         string `json:"entry"`
	Nodes         []Node `json:"nodes"`
}

// Node is edited through typed controls, never arbitrary executable content.
type Node struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	TemplateID  int64  `json:"template_id,omitempty"`
	WaitSeconds int32  `json:"wait_seconds,omitempty"`
	Field       string `json:"field,omitempty"`
	Operator    string `json:"operator,omitempty"`
	Value       string `json:"value,omitempty"`
	Next        string `json:"next,omitempty"`
	Otherwise   string `json:"otherwise,omitempty"`
}

// NodeError identifies the exact control that needs correction.
type NodeError struct {
	Node  string `json:"node"`
	Field string `json:"field"`
	Code  string `json:"code"`
}

var nodeID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
var domainValue = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

// ValidateDefinition enforces a reachable DAG and bounded relative UTC waits.
func ValidateDefinition(d Definition) []NodeError {
	problems := []NodeError{}
	add := func(id, field, code string) { problems = append(problems, NodeError{id, field, code}) }
	if d.SchemaVersion != 1 {
		add("", "schema_version", "schema_version")
	}
	if d.EventSource != "contact_enrolled" {
		add("", "event_source", "event_source")
	}
	if len(d.Nodes) < 1 || len(d.Nodes) > 20 {
		add("", "nodes", "node_limit")
		return problems
	}
	nodes := map[string]Node{}
	for _, n := range d.Nodes {
		if !nodeID.MatchString(n.ID) {
			add(n.ID, "id", "node_id")
		}
		if _, ok := nodes[n.ID]; ok {
			add(n.ID, "id", "duplicate_node")
		}
		nodes[n.ID] = n
		switch n.Kind {
		case "send":
			if n.TemplateID <= 0 {
				add(n.ID, "template_id", "template_required")
			}
		case "wait":
			if n.WaitSeconds < 1 || n.WaitSeconds > 2592000 {
				add(n.ID, "wait_seconds", "wait_limit")
			}
		case "condition":
			if n.Operator != "equals" && n.Operator != "not_equals" {
				add(n.ID, "operator", "operator")
			}
			switch n.Field {
			case "email_domain":
				if !domainValue.MatchString(n.Value) || !strings.Contains(n.Value, ".") || n.Value != strings.ToLower(n.Value) {
					add(n.ID, "value", "field_value")
				}
			case "list_id":
				if v, err := strconv.ParseInt(n.Value, 10, 64); err != nil || v <= 0 {
					add(n.ID, "value", "field_value")
				}
			case "source":
				if n.Value != "contact_enrolled" {
					add(n.ID, "value", "field_value")
				}
			case "consented":
				if n.Value != "true" && n.Value != "false" {
					add(n.ID, "value", "field_value")
				}
			default:
				add(n.ID, "field", "field")
			}
		case "stop":
			if n.Next != "" || n.Otherwise != "" {
				add(n.ID, "next", "terminal_node")
			}
		default:
			add(n.ID, "kind", "node_kind")
		}
	}
	if _, ok := nodes[d.Entry]; !ok {
		add("", "entry", "entry")
	}
	for _, n := range d.Nodes {
		if n.Kind != "stop" {
			if _, ok := nodes[n.Next]; !ok {
				add(n.ID, "next", "target")
			}
		}
		if n.Kind == "condition" {
			if _, ok := nodes[n.Otherwise]; !ok {
				add(n.ID, "otherwise", "target")
			}
		}
	}
	if len(problems) > 0 {
		return problems
	}
	state := map[string]int{}
	memo := map[string]int64{}
	var visit func(string) int64
	visit = func(id string) int64 {
		if state[id] == 1 {
			add(id, "next", "cycle")
			return 0
		}
		if state[id] == 2 {
			return memo[id]
		}
		state[id] = 1
		n := nodes[id]
		wait := int64(0)
		if n.Kind != "stop" {
			wait = visit(n.Next)
		}
		if n.Kind == "condition" {
			if other := visit(n.Otherwise); other > wait {
				wait = other
			}
		}
		if n.Kind == "wait" {
			wait += int64(n.WaitSeconds)
		}
		if wait > 7776000 && n.Kind == "wait" {
			add(id, "wait_seconds", "total_wait_limit")
		}
		state[id] = 2
		memo[id] = wait
		return wait
	}
	visit(d.Entry)
	for _, n := range d.Nodes {
		if state[n.ID] == 0 {
			add(n.ID, "id", "unreachable")
		}
	}
	return problems
}

// Condition evaluates only server-resolved event facts from the published schema.
func Condition(n Node, facts map[string]string) bool {
	matched := facts[n.Field] == n.Value
	if n.Operator == "not_equals" {
		return !matched
	}
	return matched
}
