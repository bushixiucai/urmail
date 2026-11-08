package templates

import (
	"fmt"
	"strconv"
	"strings"
)

// Batch operations are the prompt templates behind the Capabilities helper
// methods (automatic classification, summarization, translation and phishing
// scanning). They keep the exact task wording those callers have always
// sent; only the user message gains the isolation envelope.

func init() {
	Register(Template{
		Operation: "batch_classify",
		Version:   1,
		JSONMode:  true,
		System: func(p Params) string {
			return "You are an email classification assistant. Categorize the given email into exactly one of: " +
				"inbox, work, personal, marketing, finance, support, notifications, spam.\n" +
				"Respond ONLY with a JSON object in this format: {\"category\": \"<category>\", \"score\": <float between 0.0 and 1.0>}"
		},
		User: envelope(nil, func(p Params) string {
			return "Subject: " + p.Subject + "\n\nBody:\n" + p.Body
		}),
	})

	Register(Template{
		Operation: "batch_summarize",
		Version:   1,
		System: func(p Params) string {
			system := "You are a concise email summarization assistant. Provide an objective, clear summary of the email. " +
				"Respond ONLY with the summary text, with no introductory phrases or metadata."
			if p.MaxLength > 0 {
				system += " The summary must not exceed " + strconv.Itoa(p.MaxLength) + " characters."
			}
			return system
		},
		User: envelope(nil, func(p Params) string { return p.Body }),
	})

	Register(Template{
		Operation: "batch_translate",
		Version:   1,
		Validate:  requireTargetLanguage,
		System: func(p Params) string {
			return "You are a professional email translation assistant. " +
				"Translate the following email body into " + p.TargetLanguage + ". Preserve paragraph structure and tone. " +
				"Respond ONLY with the translation, with no commentary."
		},
		User: envelope(nil, func(p Params) string { return p.Body }),
	})

	Register(Template{
		Operation: "batch_phishing",
		Version:   1,
		JSONMode:  true,
		System: func(p Params) string {
			return "You are an expert cybersecurity email threat analyzer. " +
				"Evaluate the email for phishing indicators such as domain spoofing, urgent threats, credential harvesting, and deceptive links.\n" +
				"Respond ONLY with a JSON object in this format:\n" +
				"{\"phishing_score\": <float between 0.0 and 1.0>, \"reasons\": [\"<reason 1>\", \"<reason 2>\"]}\n" +
				"A score of 0.0 means completely safe; 1.0 means definite phishing threat."
		},
		User: envelope(nil, func(p Params) string {
			var linksPart string
			if len(p.Links) > 0 {
				linksPart = "\nLinks:\n- " + strings.Join(p.Links, "\n- ")
			} else {
				linksPart = "\nLinks: none"
			}
			return fmt.Sprintf("From: %s\nSubject: %s%s\n\nBody:\n%s", p.From, p.Subject, linksPart, p.Body)
		}),
	})
}
