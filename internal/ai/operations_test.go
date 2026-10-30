package ai

import "testing"

func TestOperationOutputSchemasAndNoAutomaticActions(t *testing.T) {
	cases := []struct {
		operation, good string
		bad             []string
	}{
		{"classify", `{"category":"work","score":0.9}`, []string{`{"category":"execute","score":1}`, `{"category":"work|personal","score":1}`, `{"category":"work","score":2}`, `{"category":"work"}`, `{"category":"work","score":1,"send":true}`}},
		{"summarize", `{"summary":"An email"}`, []string{`{"summary":""}`, `{"summary":"A","rule":"discard"}`}},
		{"translate", `{"translation":"Hello"}`, []string{`{"translation":""}`}},
		{"phishing", `{"score":0.2,"reasons":["No trusted authentication"]}`, []string{`{"score":-1,"reasons":[]}`, `{"reasons":[]}`, `{"score":0.1}`}},
		{"polish", `{"draft":"Review this suggestion"}`, []string{`{"draft":"text","send":true}`}},
		{"todos", `{"items":[{"task":"Send report","assignee":"Alice","due_date":"2026-11-01"}]}`, []string{`{"items":[{"task":""}]}`, `{"items":"none"}`, `{"items":[{"task":"Send","send":true}]}`}},
		{"custom", `{"task_type":"summary","result":"Here is the answer"}`, []string{`{"task_type":"illegal","result":"ok"}`, `{"task_type":"summary","result":""}`, `{"result":"missing type"}`}},
	}
	for _, test := range cases {
		t.Run(test.operation, func(t *testing.T) {
			if _, err := ValidateOperation(test.operation, test.good); err != nil {
				t.Fatal(err)
			}
			for _, bad := range test.bad {
				if _, err := ValidateOperation(test.operation, bad); err == nil {
					t.Fatalf("invalid output accepted %s", bad)
				}
			}
		})
	}
	if _, err := BuildOperation(OperationInput{Operation: "send", Body: "send it"}); err == nil {
		t.Fatal("model send operation accepted")
	}
	if _, err := ConfiguredProvider("ollama", "http://127.0.0.1:9999", "model", "", nil, []string{"http://127.0.0.1:11434"}); err == nil {
		t.Fatal("arbitrary loopback port accepted")
	}
	if _, err := ConfiguredProvider("openai", "https://api.openai.com/v1", "model", "secret", nil, nil); err == nil {
		t.Fatal("default unconfigured cloud enabled")
	}
}

func TestDraftSuggestionsRemainBoundedAndDoNotCarryRecipients(t *testing.T) {
	for _, operation := range []string{"draft_new", "draft_reply"} {
		request, err := BuildOperation(OperationInput{Operation: operation, Body: "Selected source", Draft: "My existing text", Instruction: "Write a short reply"})
		if err != nil || request.MaxTokens == nil || *request.MaxTokens != 2048 {
			t.Fatalf("unbounded draft request: %+v %v", request, err)
		}
		if _, err := ValidateOperation(operation, `{"subject":"Suggestion","body":"Editable body"}`); err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{`{"subject":"Suggestion","body":"Editable body","to":["attacker@example.test"]}`, `{"subject":"Bad\r\nBcc: attacker@example.test","body":"Body"}`, `{"subject":"Suggestion","body":"Body","send":true}`} {
			if _, err := ValidateOperation(operation, raw); err == nil {
				t.Fatalf("unsafe suggestion accepted: %s", raw)
			}
		}
	}
}
