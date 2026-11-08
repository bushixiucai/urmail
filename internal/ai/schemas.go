package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

// Structured payload validation for the AI pipeline (TECH §2). Every
// pipeline response is a single JSON object, decoded strictly (unknown
// fields rejected), clamped, and normalized: enums are whitelists, scores
// are finite [0,1], list fields never serialize as null. The legacy
// ValidateOperation keeps the assist endpoint's current shapes; the
// endpoint switches to these payloads in the contract PR together with
// frontend support.

// riskLevelFor maps a phishing score onto the public risk level. The
// model-provided enum is only kept when it agrees with this mapping, so an
// injected or hallucinated risk_level can never outrank the evidence.
func riskLevelFor(score float64) string {
	switch {
	case score < 0.3:
		return "low"
	case score < 0.6:
		return "medium"
	case score < 0.85:
		return "high"
	default:
		return "critical"
	}
}

func clampScore(score *float64) bool {
	if score == nil {
		return false
	}
	if math.IsNaN(*score) || math.IsInf(*score, 0) {
		return false
	}
	if *score < 0 {
		*score = 0
	}
	if *score > 1 {
		*score = 1
	}
	return true
}

func trimNonEmpty(s string, limit int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > limit {
		return "", false
	}
	return s, true
}

// payloadSizeCap bounds one validated payload.
const payloadSizeCap = 65536

// ValidatePayload decodes and normalizes one structured operation payload.
func ValidatePayload(operation string, raw string) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > payloadSizeCap {
		return nil, ErrInvalidResponse
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()

	var value any
	switch operation {
	case "classify":
		value = &struct {
			Category   string   `json:"category"`
			Confidence *float64 `json:"confidence"`
			Score      *float64 `json:"score"`
		}{}
	case "summarize":
		value = &struct {
			Bullets []string `json:"bullets"`
			Todos   []struct {
				Task     string  `json:"task"`
				Assignee *string `json:"assignee"`
				DueDate  *string `json:"due_date"`
			} `json:"todos"`
			KeyDates []struct {
				Date  string `json:"date"`
				Label string `json:"label"`
			} `json:"key_dates"`
			Language string `json:"language"`
		}{}
	case "draft_new", "draft_reply":
		value = &struct {
			Subject     string   `json:"subject"`
			Body        string   `json:"body"`
			ToneNotes   string   `json:"tone_notes"`
			Assumptions []string `json:"assumptions"`
		}{}
	case "polish":
		value = &struct {
			RevisedText string `json:"revised_text"`
			Changes     []struct {
				Type string `json:"type"`
				Note string `json:"note"`
			} `json:"changes"`
			Tone string `json:"tone"`
		}{}
	case "translate":
		value = &struct {
			TranslatedText string `json:"translated_text"`
			DetectedSource string `json:"detected_source"`
		}{}
	case "phishing":
		value = &struct {
			Score      *float64 `json:"score"`
			RiskLevel  string   `json:"risk_level"`
			Reasons    []string `json:"reasons"`
			Indicators []struct {
				Type     string `json:"type"`
				Evidence string `json:"evidence"`
			} `json:"indicators"`
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
		return nil, fmt.Errorf("%w: unknown operation %q", ErrInvalidRequest, operation)
	}

	if err := decoder.Decode(value); err != nil {
		return nil, fmt.Errorf("%w: %s schema: %w", ErrInvalidResponse, operation, err)
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return nil, fmt.Errorf("%w: %s trailing content", ErrInvalidResponse, operation)
	}
	return normalizePayload(operation, value)
}

// normalizePayload enforces per-operation bounds and emits the normalized
// JSON the API layer returns. Everything past this point is trusted data.
func normalizePayload(operation string, value any) (json.RawMessage, error) {
	invalid := func(detail string) error {
		return fmt.Errorf("%w: %s %s", ErrInvalidResponse, operation, detail)
	}
	// scoreOf/shared helpers keep the switch bodies small.
	switch v := value.(type) {
	case *struct {
		Category   string   `json:"category"`
		Confidence *float64 `json:"confidence"`
		Score      *float64 `json:"score"`
	}:
		if !validCategory(v.Category) {
			return nil, invalid("category")
		}
		conf := v.Confidence
		if conf == nil {
			conf = v.Score
		}
		if !clampScore(conf) {
			return nil, invalid("confidence")
		}
		// score stays for one compatibility release.
		return marshalJSON(map[string]any{
			"category":   v.Category,
			"confidence": round3(*conf),
			"score":      round3(*conf),
		})

	case *struct {
		Bullets []string `json:"bullets"`
		Todos   []struct {
			Task     string  `json:"task"`
			Assignee *string `json:"assignee"`
			DueDate  *string `json:"due_date"`
		} `json:"todos"`
		KeyDates []struct {
			Date  string `json:"date"`
			Label string `json:"label"`
		} `json:"key_dates"`
		Language string `json:"language"`
	}:
		if len(v.Bullets) == 0 || len(v.Bullets) > 6 {
			return nil, invalid("bullets count")
		}
		bullets := make([]string, 0, len(v.Bullets))
		for _, b := range v.Bullets {
			s, ok := trimNonEmpty(b, 200)
			if !ok {
				return nil, invalid("bullet")
			}
			bullets = append(bullets, s)
		}
		if len(v.Todos) > 10 || len(v.KeyDates) > 10 {
			return nil, invalid("list size")
		}
		todos := make([]map[string]any, 0, len(v.Todos))
		for _, td := range v.Todos {
			task, ok := trimNonEmpty(td.Task, 500)
			if !ok {
				return nil, invalid("todo task")
			}
			item := map[string]any{"task": task, "assignee": nil, "due_date": nil}
			if td.Assignee != nil {
				if s, ok := trimNonEmpty(*td.Assignee, 500); ok {
					item["assignee"] = s
				}
			}
			if td.DueDate != nil {
				if s, ok := trimNonEmpty(*td.DueDate, 100); ok {
					item["due_date"] = s
				}
			}
			todos = append(todos, item)
		}
		dates := make([]map[string]any, 0, len(v.KeyDates))
		for _, kd := range v.KeyDates {
			date, ok := trimNonEmpty(kd.Date, 40)
			if !ok {
				return nil, invalid("key date")
			}
			label := strings.TrimSpace(kd.Label)
			if len(label) > 200 {
				return nil, invalid("key date label")
			}
			dates = append(dates, map[string]any{"date": date, "label": label})
		}
		language := strings.TrimSpace(v.Language)
		if len(language) > 16 {
			return nil, invalid("language tag")
		}
		if language == "" {
			language = "unknown"
		}
		return marshalJSON(map[string]any{
			"bullets":   bullets,
			"todos":     todos,
			"key_dates": dates,
			"language":  language,
		})

	case *struct {
		Subject     string   `json:"subject"`
		Body        string   `json:"body"`
		ToneNotes   string   `json:"tone_notes"`
		Assumptions []string `json:"assumptions"`
	}:
		subject, ok := trimNonEmpty(v.Subject, 200)
		if !ok || strings.ContainsAny(subject, "\r\n") {
			return nil, invalid("subject")
		}
		body, ok := trimNonEmpty(v.Body, 20000)
		if !ok {
			return nil, invalid("body")
		}
		notes := strings.TrimSpace(v.ToneNotes)
		if len(notes) > 200 {
			return nil, invalid("tone_notes")
		}
		if len(v.Assumptions) > 5 {
			return nil, invalid("assumptions count")
		}
		assumptions := make([]string, 0, len(v.Assumptions))
		for _, a := range v.Assumptions {
			s, ok := trimNonEmpty(a, 300)
			if !ok {
				return nil, invalid("assumption")
			}
			assumptions = append(assumptions, s)
		}
		return marshalJSON(map[string]any{
			"subject":     subject,
			"body":        body,
			"tone_notes":  notes,
			"assumptions": assumptions,
		})

	case *struct {
		RevisedText string `json:"revised_text"`
		Changes     []struct {
			Type string `json:"type"`
			Note string `json:"note"`
		} `json:"changes"`
		Tone string `json:"tone"`
	}:
		revised, ok := trimNonEmpty(v.RevisedText, 30000)
		if !ok {
			return nil, invalid("revised_text")
		}
		if len(v.Changes) > 10 {
			return nil, invalid("changes count")
		}
		changes := make([]map[string]any, 0, len(v.Changes))
		for _, c := range v.Changes {
			switch c.Type {
			case "clarity", "grammar", "tone", "structure":
			default:
				return nil, invalid("change type")
			}
			note := strings.TrimSpace(c.Note)
			if len(note) > 300 {
				return nil, invalid("change note")
			}
			changes = append(changes, map[string]any{"type": c.Type, "note": note})
		}
		tone := strings.TrimSpace(v.Tone)
		if tone == "" {
			tone = "neutral"
		}
		switch tone {
		case "neutral", "formal", "concise", "friendly":
		default:
			return nil, invalid("tone")
		}
		return marshalJSON(map[string]any{
			"revised_text": revised,
			"changes":      changes,
			"tone":         tone,
		})

	case *struct {
		TranslatedText string `json:"translated_text"`
		DetectedSource string `json:"detected_source"`
	}:
		text, ok := trimNonEmpty(v.TranslatedText, 60000)
		if !ok {
			return nil, invalid("translated_text")
		}
		source := strings.TrimSpace(v.DetectedSource)
		if len(source) > 16 {
			return nil, invalid("detected_source")
		}
		return marshalJSON(map[string]any{"translated_text": text, "detected_source": source})

	case *struct {
		Score      *float64 `json:"score"`
		RiskLevel  string   `json:"risk_level"`
		Reasons    []string `json:"reasons"`
		Indicators []struct {
			Type     string `json:"type"`
			Evidence string `json:"evidence"`
		} `json:"indicators"`
	}:
		if !clampScore(v.Score) {
			return nil, invalid("score")
		}
		if len(v.Reasons) > 20 {
			return nil, invalid("reasons count")
		}
		reasons := make([]string, 0, len(v.Reasons))
		for _, r := range v.Reasons {
			s, ok := trimNonEmpty(r, 2000)
			if !ok {
				return nil, invalid("reason")
			}
			reasons = append(reasons, s)
		}
		if len(v.Indicators) > 10 {
			return nil, invalid("indicators count")
		}
		indicators := make([]map[string]any, 0, len(v.Indicators))
		for _, ind := range v.Indicators {
			switch ind.Type {
			case "spoofed_domain", "suspicious_link", "urgent_pressure", "credential_request", "other":
			default:
				return nil, invalid("indicator type")
			}
			evidence := strings.TrimSpace(ind.Evidence)
			if len(evidence) > 2000 {
				return nil, invalid("indicator evidence")
			}
			indicators = append(indicators, map[string]any{"type": ind.Type, "evidence": evidence})
		}
		return marshalJSON(map[string]any{
			"score":      round3(*v.Score),
			"risk_level": riskLevelFor(*v.Score), // score-mapped: enum from the model is never trusted over evidence
			"reasons":    reasons,
			"indicators": indicators,
		})

	case *struct {
		Items []struct {
			Task     string  `json:"task"`
			Assignee *string `json:"assignee"`
			DueDate  *string `json:"due_date"`
		} `json:"items"`
	}:
		if v.Items == nil {
			return nil, invalid("items")
		}
		if len(v.Items) > 30 {
			return nil, invalid("items count")
		}
		items := make([]map[string]any, 0, len(v.Items))
		for _, item := range v.Items {
			task, ok := trimNonEmpty(item.Task, 2000)
			if !ok {
				return nil, invalid("task")
			}
			entry := map[string]any{"task": task, "assignee": nil, "due_date": nil}
			if item.Assignee != nil {
				if s, ok := trimNonEmpty(*item.Assignee, 500); ok {
					entry["assignee"] = s
				}
			}
			if item.DueDate != nil {
				if s, ok := trimNonEmpty(*item.DueDate, 100); ok {
					entry["due_date"] = s
				}
			}
			items = append(items, entry)
		}
		return marshalJSON(map[string]any{"items": items})

	case *struct {
		TaskType string `json:"task_type"`
		Result   string `json:"result"`
	}:
		switch v.TaskType {
		case "extraction", "explanation", "summary", "translation", "draft", "other":
		default:
			return nil, invalid("task_type")
		}
		result, ok := trimNonEmpty(v.Result, 30000)
		if !ok {
			return nil, invalid("result")
		}
		return marshalJSON(map[string]any{"task_type": v.TaskType, "result": result})

	default:
		return nil, fmt.Errorf("%w: %s schema mismatch", ErrInvalidResponse, operation)
	}
}

// round3 keeps scores stable across JSON round-trips.
func round3(f float64) float64 {
	return math.Round(f*1000) / 1000
}

// marshalJSON serializes a normalized payload; map ordering is handled by
// encoding/json (keys sorted), so output is deterministic.
func marshalJSON(v any) (json.RawMessage, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: normalize: %w", ErrInvalidResponse, err)
	}
	return out, nil
}
