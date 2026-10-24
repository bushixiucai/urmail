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
