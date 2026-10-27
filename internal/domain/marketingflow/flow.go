// Package marketingflow implements domain rules for marketingflow.
package marketingflow

import "errors"

// ErrInvalidFlow is the invalid flow error.
var ErrInvalidFlow = errors.New("invalid marketing flow")

// Step is one user-visible automation action. Sending is not performed here.
type Step struct {
	Key              string
	Kind             string
	Next             string
	WhenOpened       string
	WhenUnsubscribed string
}

// Flow is a bounded sequence with explicit branches.
type Flow struct{ Steps []Step }

// Normalize implements the corresponding domain rule.
func Normalize(in Flow) (Flow, error) {
	if len(in.Steps) == 0 || len(in.Steps) > 20 {
		return Flow{}, ErrInvalidFlow
	}
	seen := map[string]Step{}
	for _, step := range in.Steps {
		if step.Key == "" || len(step.Key) > 40 || seen[step.Key].Key != "" {
			return Flow{}, ErrInvalidFlow
		}
		switch step.Kind {
		case "send", "wait", "stop":
		default:
			return Flow{}, ErrInvalidFlow
		}
		seen[step.Key] = step
	}
	for _, step := range in.Steps {
		for _, target := range []string{step.Next, step.WhenOpened, step.WhenUnsubscribed} {
			if target != "" && seen[target].Key == "" {
				return Flow{}, ErrInvalidFlow
			}
		}
	}
	if _, err := Advance(in, in.Steps[0].Key, "none"); err != nil {
		return Flow{}, err
	}
	return in, nil
}

// Advance chooses the next step. Unsubscribe always wins over opened.
func Advance(flow Flow, current, event string) (string, error) {
	var step Step
	found := false
	for _, item := range flow.Steps {
		if item.Key == current {
			step = item
			found = true
		}
	}
	if !found {
		return "", ErrInvalidFlow
	}
	if step.Kind == "stop" {
		return step.Key, nil
	}
	switch event {
	case "unsubscribed":
		if step.WhenUnsubscribed != "" {
			return step.WhenUnsubscribed, nil
		}
		return "stop", nil
	case "opened":
		if step.WhenOpened != "" {
			return step.WhenOpened, nil
		}
	}
	if step.Next == "" {
		return "stop", nil
	}
	return step.Next, nil
}
