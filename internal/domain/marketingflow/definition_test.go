package marketingflow

import "testing"

func validDefinition() Definition {
	return Definition{SchemaVersion: 1, EventSource: "contact_enrolled", Entry: "branch", Nodes: []Node{
		{ID: "branch", Kind: "condition", Field: "email_domain", Operator: "equals", Value: "example.test", Next: "send", Otherwise: "stop"},
		{ID: "send", Kind: "send", TemplateID: 1, Next: "wait"}, {ID: "wait", Kind: "wait", WaitSeconds: 3600, Next: "stop"}, {ID: "stop", Kind: "stop"}}}
}
func TestDefinitionRejectsUnsafeGraphAndFields(t *testing.T) {
	if p := ValidateDefinition(validDefinition()); len(p) != 0 {
		t.Fatal(p)
	}
	cases := []struct {
		name string
		edit func(*Definition)
		code string
	}{
		{"cycle", func(d *Definition) { d.Nodes[2].Next = "branch" }, "cycle"},
		{"invalid_field", func(d *Definition) { d.Nodes[0].Field = "customer.secret" }, "field"},
		{"invalid_source", func(d *Definition) { d.EventSource = "payment_from_client" }, "event_source"},
		{"unreachable", func(d *Definition) { d.Nodes = append(d.Nodes, Node{ID: "orphan", Kind: "stop"}) }, "unreachable"},
		{"oversize_wait", func(d *Definition) { d.Nodes[2].WaitSeconds = 2592001 }, "wait_limit"},
		{"missing_template", func(d *Definition) { d.Nodes[1].TemplateID = 0 }, "template_required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := validDefinition()
			c.edit(&d)
			for _, p := range ValidateDefinition(d) {
				if p.Code == c.code {
					return
				}
			}
			t.Fatalf("missing %s", c.code)
		})
	}
}
func TestConditionUsesOnlyResolvedEventFacts(t *testing.T) {
	n := validDefinition().Nodes[0]
	if !Condition(n, map[string]string{"email_domain": "example.test"}) || Condition(n, map[string]string{"email_domain": "other.test"}) {
		t.Fatal("condition branch mismatch")
	}
	n.Operator = "not_equals"
	if !Condition(n, map[string]string{"email_domain": "other.test"}) {
		t.Fatal("negative branch mismatch")
	}
}
