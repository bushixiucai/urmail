package templates

import "strings"

// Structured (v2) generation of the assist operations (TECH §2). These
// templates carry the new output schemas — bullets/key_dates for summarize,
// confidence for classify, tone_notes/assumptions for drafts, changes for
// polish, detected_source for translate, risk_level/indicators for phishing
// — validated by ai.ValidatePayload. They are reachable only through the
// pipeline mapping table until the contract PR switches the endpoint over,
// at which point the v1 assist entries retire.

// structuredSchema renders the shared system message for a v2 operation.
func structuredSchema(taskConstraint, schema string) string {
	return jsonSystem(taskConstraint, schema)
}

func init() {
	Register(Template{
		Operation: "classify_v2",
		Version:   2,
		JSONMode:  true,
		MaxTokens: 2048,
		System: func(p Params) string {
			return structuredSchema("Classify the email category. Scores must be finite between 0 and 1. Report your confidence in the classification.",
				`{"category":"inbox|work|personal|marketing|finance|support|notifications|spam","confidence":0.0}`)
		},
		User: envelope(nil, emailContent),
	})

	Register(Template{
		Operation: "summarize_v2",
		Version:   2,
		JSONMode:  true,
		MaxTokens: 2048,
		System: func(p Params) string {
			return structuredSchema("Summarize the key facts strictly from the email text. Never fabricate facts, dates, people or deadlines; if facts are absent or ambiguous, leave the corresponding lists empty.",
				`{"bullets":["key point"],"todos":[{"task":"action item","assignee":"name or null","due_date":"date or null"}],"key_dates":[{"date":"YYYY-MM-DD","label":"what happens"}],"language":"BCP 47 tag of the email body language"}`)
		},
		User: envelope(nil, emailContent),
	})

	registerStructuredDrafts()

	Register(Template{
		Operation: "polish_v2",
		Version:   2,
		JSONMode:  true,
		MaxTokens: 2048,
		System: func(p Params) string {
			return structuredSchema("Refine the provided draft for clarity and professional tone without altering factual commitments. Explain each change you made.",
				`{"revised_text":"revised draft","changes":[{"type":"clarity|grammar|tone|structure","note":"what changed"}],"tone":"neutral|formal|concise|friendly"}`)
		},
		User: envelope(nil, func(p Params) string {
			content := p.Draft
			if p.Body != "" {
				content += "\n\n" + emailContent(p)
			}
			return content
		}),
	})

	Register(Template{
		Operation: "translate_v2",
		Version:   2,
		Validate:  requireTargetLanguage,
		JSONMode:  true,
		MaxTokens: 2048,
		System: func(p Params) string {
			return structuredSchema("Translate the email text faithfully into the target language ("+p.TargetLanguage+"). Preserve formatting, proper nouns, numbers, currencies, dates, and technical terms accurately without modification. Also report the source language you detected.",
				`{"translated_text":"translated text","detected_source":"BCP 47 source language tag"}`)
		},
		User: envelope(func(p Params) []string {
			return []string{"target_language: " + p.TargetLanguage}
		}, emailContent),
	})

	Register(Template{
		Operation: "phishing_v2",
		Version:   2,
		JSONMode:  true,
		MaxTokens: 2048,
		System: func(p Params) string {
			return structuredSchema("Evaluate the email for phishing or security risks based on verifiable evidence only. Scores must be finite between 0 and 1. risk_level must be consistent with the score. If an element cannot be verified, state that explicitly.",
				`{"score":0.0,"risk_level":"low|medium|high|critical","reasons":["reason"],"indicators":[{"type":"spoofed_domain|suspicious_link|urgent_pressure|credential_request|other","evidence":"quoted evidence"}]}`)
		},
		User: envelope(nil, emailContent),
	})

	Register(Template{
		// Same shape as the legacy todos schema; registered as its own
		// generation so the pipeline rollout is one explicit table.
		Operation: "todos_v2",
		Version:   2,
		JSONMode:  true,
		MaxTokens: 2048,
		System: func(p Params) string {
			return structuredSchema("Extract concrete action items from the email. Only include an assignee or due date if explicitly stated in the email text; never fabricate unknown deadlines or assignees.",
				`{"items":[{"task":"action item text","assignee":"person name or null if unspecified","due_date":"date or null if unspecified"}]}`)
		},
		User: envelope(nil, emailContent),
	})

	Register(Template{
		Operation: "custom_v2",
		Version:   2,
		JSONMode:  true,
		MaxTokens: 2048,
		System: func(p Params) string {
			return structuredSchema("Execute the user instruction regarding the email. Do not execute actions, send messages or create rules.",
				`{"task_type":"extraction|explanation|summary|translation|draft|other","result":"task result"}`)
		},
		User: envelope(instructionParams, emailContent),
	})
}

// registerStructuredDrafts adds the draft_new/draft_reply v2 pair.
func registerStructuredDrafts() {
	draftSystem := func(p Params) string {
		return structuredSchema("Suggest a draft reply based strictly on the email and instructions. Do not invent factual commitments, contractual promises, or unauthorized deadlines. Suggestion only; never send. State the assumptions you made so the sender can review them.",
			`{"subject":"suggested subject","body":"suggested body; never send it","tone_notes":"tone rationale","assumptions":["assumption made"]}`)
	}
	draftUser := envelope(instructionParams, func(p Params) string {
		content := emailContent(p)
		if strings.TrimSpace(p.Draft) != "" {
			content += "\n\nDRAFT_UNDER_REVIEW:\n" + p.Draft
		}
		return content
	})
	Register(Template{Operation: "draft_new_v2", Version: 2, JSONMode: true, MaxTokens: 2048, System: draftSystem, User: draftUser})
	Register(Template{Operation: "draft_reply_v2", Version: 2, JSONMode: true, MaxTokens: 2048, System: draftSystem, User: draftUser})
}
