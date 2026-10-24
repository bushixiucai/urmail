package handlers

import (
	"encoding/json"
	"testing"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestAnnouncementDTOEmitsLocalizedObjectsInsteadOfBase64(t *testing.T) {
	raw, err := json.Marshal(announcementDTO(store.Announcements{ID: 1, Title: []byte(`{"en":"Title","zh-CN":"标题"}`), Body: []byte(`{"en":"Body","zh-CN":"正文"}`)}))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Title map[string]string `json:"title"`
		Body  map[string]string `json:"body"`
	}
	if err = json.Unmarshal(raw, &result); err != nil || result.Title["zh-CN"] != "标题" || result.Body["en"] != "Body" {
		t.Fatalf("localized announcement JSON invalid: %s %v", raw, err)
	}
}
