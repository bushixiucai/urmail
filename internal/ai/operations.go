package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/bushixiucai/urmail/internal/ai/templates"
)

// OperationInput contains only the message data explicitly selected by its owner.
type OperationInput struct {
	Draft          string `json:"draft,omitempty"`
	Instruction    string `json:"instruction,omitempty"`
	Operation      string `json:"operation"`
	Subject        string `json:"subject,omitempty"`
	Body           string `json:"body"`
	From           string `json:"from,omitempty"`
	TargetLanguage string `json:"target_language,omitempty"`
	// LengthLimit is the per-operation output length option (DEF-A): the
	// summarize "shorter" control sends it as a per-bullet character cap.
	// It rides the digest folded over the whole input, so two calls that
	// differ only here are different cache keys. Normalize it with
	// NormalizeLengthLimit before storing or hashing.
	LengthLimit int `json:"length_limit,omitempty"`
}

// LengthLimitCeiling bounds the summarize length option; anything above
// clamps down so a stray client value cannot stretch the prompt.
const LengthLimitCeiling = 1000

// NormalizeLengthLimit folds raw client length options onto the canonical
// form: negative values mean "use the default" (0), values above the
// ceiling clamp to it. Only the normalized value may enter prompt
// assembly or a cache digest, so "-5" and "0" can never split one cache
// entry in two.
func NormalizeLengthLimit(raw int) int {
	if raw < 0 {
		return 0
	}
	if raw > LengthLimitCeiling {
		return LengthLimitCeiling
	}
	return raw
}

// BuildOperation creates one bounded suggestion-only model request. Prompt
// assembly lives in the versioned templates registry; this boundary keeps
// the input bounds and maps template errors onto the API error contract.
func BuildOperation(input OperationInput) (CompletionRequest, error) {
	if len(input.Draft) > 65536 || len(input.Instruction) > 4000 || len(input.Body) == 0 || len(input.Body) > 65536 || len(input.Subject) > 1000 || len(input.From) > 1000 || len(input.TargetLanguage) > 64 {
		return CompletionRequest{}, ErrInvalidRequest
	}
	treq, err := templates.Build(input.Operation, templates.Params{
		Draft:          input.Draft,
		Instruction:    input.Instruction,
		Subject:        input.Subject,
		Body:           input.Body,
		From:           input.From,
		TargetLanguage: input.TargetLanguage,
		LengthLimit:    input.LengthLimit,
	})
	if err != nil {
		// Unknown operations and missing parameters were always request
		// errors at this boundary.
		return CompletionRequest{}, ErrInvalidRequest
	}
	req := CompletionRequest{
		Messages: make([]Message, len(treq.Messages)),
		MaxTokens: func() *int {
			if treq.MaxTokens <= 0 {
				return nil
			}
			mt := treq.MaxTokens
			return &mt
		}(),
		JSONMode: treq.JSONMode,
	}
	if treq.Temperature != nil {
		t := *treq.Temperature
		req.Temperature = &t
	}
	for i, m := range treq.Messages {
		req.Messages[i] = Message{Role: m.Role, Content: m.Content}
	}
	return req, nil
}

// ValidateOperation rejects schema violations; it never executes model output.
func ValidateOperation(operation, raw string) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 65536 {
		return nil, ErrInvalidResponse
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var value any
	switch operation {
	case "classify":
		value = &struct {
			Category string   `json:"category"`
			Score    *float64 `json:"score"`
		}{}
	case "summarize":
		value = &struct {
			Summary string `json:"summary"`
		}{}
	case "translate":
		value = &struct {
			Translation string `json:"translation"`
		}{}
	case "phishing":
		value = &struct {
			Score   *float64 `json:"score"`
			Reasons []string `json:"reasons"`
		}{}
	case "draft_new", "draft_reply":
		value = &struct {
			Subject string `json:"subject"`
			Body    string `json:"body"`
		}{}
	case "polish":
		value = &struct {
			Draft string `json:"draft"`
		}{}
	case "todos":
		value = &struct {
			Items []struct {
				Task     string  `json:"task"`
				Assignee *string `json:"assignee"`
				DueDate  *string `json:"due_date"`
			} `json:"items"`
		}{}
	case "custom":
		value = &struct {
			TaskType string `json:"task_type"`
			Result   string `json:"result"`
		}{}
	default:
		return nil, ErrInvalidRequest
	}
	if decoder.Decode(value) != nil {
		return nil, ErrInvalidResponse
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return nil, ErrInvalidResponse
	}
	validScore := func(score *float64) bool {
		return score != nil && !math.IsNaN(*score) && !math.IsInf(*score, 0) && *score >= 0 && *score <= 1
	}
	switch v := value.(type) {
	case *struct {
		Category string   `json:"category"`
		Score    *float64 `json:"score"`
	}:
		if !validScore(v.Score) || !validCategory(v.Category) {
			return nil, ErrInvalidResponse
		}
	case *struct {
		Summary string `json:"summary"`
	}:
		if strings.TrimSpace(v.Summary) == "" {
			return nil, ErrInvalidResponse
		}
	case *struct {
		Translation string `json:"translation"`
	}:
		if strings.TrimSpace(v.Translation) == "" {
			return nil, ErrInvalidResponse
		}
	case *struct {
		Draft string `json:"draft"`
	}:
		if strings.TrimSpace(v.Draft) == "" {
			return nil, ErrInvalidResponse
		}
	case *struct {
		Score   *float64 `json:"score"`
		Reasons []string `json:"reasons"`
	}:
		if !validScore(v.Score) || v.Reasons == nil || len(v.Reasons) > 20 {
			return nil, ErrInvalidResponse
		}
		for _, reason := range v.Reasons {
			if len(reason) > 2000 {
				return nil, ErrInvalidResponse
			}
		}
	case *struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}:
		if strings.TrimSpace(v.Subject) == "" || len(v.Subject) > 200 || strings.ContainsAny(v.Subject, "\r\n") || strings.TrimSpace(v.Body) == "" || len(v.Body) > 20000 {
			return nil, ErrInvalidResponse
		}
	case *struct {
		Items []struct {
			Task     string  `json:"task"`
			Assignee *string `json:"assignee"`
			DueDate  *string `json:"due_date"`
		} `json:"items"`
	}:
		if v.Items == nil {
			return nil, ErrInvalidResponse
		}
		for _, item := range v.Items {
			if strings.TrimSpace(item.Task) == "" || len(item.Task) > 2000 {
				return nil, ErrInvalidResponse
			}
			if item.Assignee != nil && len(*item.Assignee) > 500 {
				return nil, ErrInvalidResponse
			}
			if item.DueDate != nil && len(*item.DueDate) > 100 {
				return nil, ErrInvalidResponse
			}
		}
	case *struct {
		TaskType string `json:"task_type"`
		Result   string `json:"result"`
	}:
		if strings.TrimSpace(v.Result) == "" || len(v.Result) > 30000 {
			return nil, ErrInvalidResponse
		}
		switch v.TaskType {
		case "extraction", "explanation", "summary", "translation", "draft", "other":
		default:
			return nil, ErrInvalidResponse
		}

	default:
		return nil, fmt.Errorf("%w: schema mismatch", ErrInvalidResponse)
	}
	normalized, _ := json.Marshal(value)
	return normalized, nil
}

func validCategory(category string) bool {
	switch category {
	case "inbox", "work", "personal", "marketing", "finance", "support", "notifications", "spam":
		return true
	default:
		return false
	}
}
