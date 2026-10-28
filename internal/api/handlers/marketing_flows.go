package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	flow "github.com/bushixiucai/urmail/internal/domain/marketingflow"
	"github.com/bushixiucai/urmail/internal/store"
)

// marketingFlowHandler authorizes tenant-authored definitions independently of
// the platform transport configuration. Every write uses the request transaction.
type marketingFlowHandler struct {
	store         *store.Queries
	workerEnabled bool
}

// RegisterMarketingFlowRoutes expects the authenticated tenant transaction group.
func RegisterMarketingFlowRoutes(group *gin.RouterGroup, q *store.Queries, enabled ...bool) {
	h := &marketingFlowHandler{store: q, workerEnabled: len(enabled) > 0 && enabled[0]}
	routes := group.Group("/marketing/flows", middleware.RequireRole("owner", "admin"))
	routes.GET("", h.list)
	routes.POST("", h.create)
	routes.GET("/:id", h.get)
	routes.PUT("/:id", h.save)
	routes.POST("/:id/preview", h.preview)
	routes.POST("/:id/publish", middleware.RateLimitWindow(10, time.Minute, middleware.TenantRateKey), h.publish)
	routes.POST("/:id/deactivate", h.deactivate)
	routes.GET("/:id/versions", h.versions)
	routes.GET("/:id/versions/:version", h.version)
	routes.POST("/:id/events", middleware.RateLimitWindow(10, time.Minute, middleware.TenantRateKey), h.enroll)
	routes.GET("/:id/instances", h.instances)
	routes.GET("/:id/instances/:instance_id/executions", h.executions)
	routes.POST("/:id/instances/:instance_id/retry", h.retry)
	routes.POST("/:id/instances/:instance_id/stop", h.stop)
}

func decodeMarketingFlow(c *gin.Context, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 65536))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		authError(c, 400, "INVALID_PARAMS")
		return false
	}
	return true
}
func flowDefinitionResponse(row store.MarketingFlowDefinitions) gin.H {
	return gin.H{"id": row.ID, "name": row.Name, "draft": json.RawMessage(row.Draft), "draft_revision": row.DraftRevision, "status": row.Status, "published_version": row.PublishedVersion, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt, "reentry_policy": "once_per_recipient_per_version", "deactivation_policy": "pause_all_existing_versions"}
}
func flowFailure(c *gin.Context, err error) bool {
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
		return true
	}
	if err != nil {
		authError(c, 500, "DB_ERROR")
		return true
	}
	return false
}
func (h *marketingFlowHandler) definition(c *gin.Context) (*store.Queries, store.MarketingFlowDefinitions, bool) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return nil, store.MarketingFlowDefinitions{}, false
	}
	id, ok := marketingID(c, "id")
	if !ok {
		return nil, store.MarketingFlowDefinitions{}, false
	}
	q := requestStore(c, h.store)
	row, err := q.GetMarketingFlowDefinition(c, store.GetMarketingFlowDefinitionParams{TenantID: tenant, ID: id})
	if flowFailure(c, err) {
		return nil, row, false
	}
	return q, row, true
}
func (h *marketingFlowHandler) list(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	rows, err := requestStore(c, h.store).ListMarketingFlowDefinitions(c, tenant)
	if flowFailure(c, err) {
		return
	}
	c.JSON(200, gin.H{"flows": rows, "worker_enabled": h.workerEnabled})
}

type flowDraftInput struct {
	Name             string          `json:"name"`
	Draft            flow.Definition `json:"draft"`
	ExpectedRevision int64           `json:"expected_revision"`
}

func boundedDraft(d flow.Definition) bool {
	return d.SchemaVersion == 1 && d.EventSource == "contact_enrolled" && len(d.Nodes) > 0 && len(d.Nodes) <= 20
}
func (h *marketingFlowHandler) create(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	var input flowDraftInput
	if !decodeMarketingFlow(c, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !marketingNameValid(input.Name) || !boundedDraft(input.Draft) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	q := requestStore(c, h.store)
	count, err := q.CountMarketingFlowDefinitions(c, tenant)
	if flowFailure(c, err) {
		return
	}
	if count >= 100 {
		authError(c, 409, "FLOW_BUDGET_EXCEEDED")
		return
	}
	encoded, _ := json.Marshal(input.Draft)
	row, err := q.CreateMarketingFlowDefinition(c, store.CreateMarketingFlowDefinitionParams{TenantID: tenant, Name: input.Name, Draft: encoded})
	if flowFailure(c, err) {
		return
	}
	c.JSON(201, gin.H{"flow": flowDefinitionResponse(row)})
}
func (h *marketingFlowHandler) get(c *gin.Context) {
	_, row, ok := h.definition(c)
	if ok {
		c.JSON(200, gin.H{"flow": flowDefinitionResponse(row)})
	}
}
func (h *marketingFlowHandler) save(c *gin.Context) {
	q, row, ok := h.definition(c)
	if !ok {
		return
	}
	var input flowDraftInput
	if !decodeMarketingFlow(c, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !marketingNameValid(input.Name) || !boundedDraft(input.Draft) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if input.ExpectedRevision != row.DraftRevision {
		authError(c, 409, "FLOW_REVISION_CONFLICT")
		return
	}
	encoded, _ := json.Marshal(input.Draft)
	saved, err := q.UpdateMarketingFlowDefinition(c, store.UpdateMarketingFlowDefinitionParams{TenantID: row.TenantID, ID: row.ID, Name: input.Name, Draft: encoded, DraftRevision: input.ExpectedRevision})
	if flowFailure(c, err) {
		return
	}
	c.JSON(200, gin.H{"flow": flowDefinitionResponse(saved)})
}
func resolveFlowTemplates(c *gin.Context, q *store.Queries, tenant int64, d flow.Definition) (map[string]store.GetMarketingFlowTemplateRow, []flow.NodeError, error) {
	problems := flow.ValidateDefinition(d)
	templates := map[string]store.GetMarketingFlowTemplateRow{}
	if len(problems) > 0 {
		return templates, problems, nil
	}
	for _, n := range d.Nodes {
		if n.Kind != "send" {
			continue
		}
		template, err := q.GetMarketingFlowTemplate(c, store.GetMarketingFlowTemplateParams{TenantID: tenant, ID: n.TemplateID})
		if errors.Is(err, pgx.ErrNoRows) {
			problems = append(problems, flow.NodeError{Node: n.ID, Field: "template_id", Code: "template_required"})
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		templates[n.ID] = template
	}
	return templates, problems, nil
}
func (h *marketingFlowHandler) preview(c *gin.Context) {
	q, row, ok := h.definition(c)
	if !ok {
		return
	}
	var input struct {
		Draft       flow.Definition `json:"draft"`
		SampleEmail string          `json:"sample_email"`
		ListID      int64           `json:"list_id"`
		At          string          `json:"at"`
	}
	if !decodeMarketingFlow(c, &input) {
		return
	}
	address, err := mail.ParseAddress(input.SampleEmail)
	if err != nil || address.Address != input.SampleEmail {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if input.ListID > 0 {
		if _, err = q.GetMarketingFlowList(c, store.GetMarketingFlowListParams{TenantID: row.TenantID, ID: input.ListID}); flowFailure(c, err) {
			return
		}
	}
	at := time.Now().UTC()
	if input.At != "" {
		at, err = time.Parse(time.RFC3339, input.At)
		if err != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	templates, problems, err := resolveFlowTemplates(c, q, row.TenantID, input.Draft)
	if flowFailure(c, err) {
		return
	}
	if len(problems) > 0 {
		flowValidationErrors(c, problems)
		return
	}
	facts := map[string]string{"source": "contact_enrolled", "email_domain": strings.ToLower(input.SampleEmail[strings.LastIndex(input.SampleEmail, "@")+1:]), "list_id": strconv.FormatInt(input.ListID, 10), "consented": "true"}
	nodes := map[string]flow.Node{}
	for _, n := range input.Draft.Nodes {
		nodes[n.ID] = n
	}
	path := []gin.H{}
	key := input.Draft.Entry
	for len(path) < 20 {
		n := nodes[key]
		step := gin.H{"node": n.ID, "kind": n.Kind, "at": at.UTC().Format(time.RFC3339)}
		switch n.Kind {
		case "send":
			content := templates[n.ID]
			step["subject"] = content.Subject
			step["text_body"] = content.TextBody
		case "wait":
			step["wait_seconds"] = n.WaitSeconds
			at = at.Add(time.Duration(n.WaitSeconds) * time.Second)
			step["due_at"] = at.UTC().Format(time.RFC3339)
		case "condition":
			step["matched"] = flow.Condition(n, facts)
			if !flow.Condition(n, facts) {
				n.Next = n.Otherwise
			}
		}
		path = append(path, step)
		if n.Kind == "stop" {
			break
		}
		key = n.Next
	}
	c.JSON(200, gin.H{"path": path, "side_effects": false, "facts": facts})
}
func (h *marketingFlowHandler) publish(c *gin.Context) {
	q, row, ok := h.definition(c)
	if !ok {
		return
	}
	var input struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decodeMarketingFlow(c, &input) {
		return
	}
	if input.ExpectedRevision != row.DraftRevision {
		authError(c, 409, "FLOW_REVISION_CONFLICT")
		return
	}
	existing, err := q.GetMarketingFlowVersionForRevision(c, store.GetMarketingFlowVersionForRevisionParams{TenantID: row.TenantID, DefinitionID: row.ID, DraftRevision: row.DraftRevision})
	if err == nil {
		if row.Status != "active" {
			authError(c, 409, "FLOW_REVISION_CONFLICT")
			return
		}
		c.JSON(200, gin.H{"version": existing.Version, "duplicate": true})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		flowFailure(c, err)
		return
	}
	var d flow.Definition
	if json.Unmarshal(row.Draft, &d) != nil {
		authError(c, 422, "INVALID_PARAMS")
		return
	}
	templates, problems, err := resolveFlowTemplates(c, q, row.TenantID, d)
	if flowFailure(c, err) {
		return
	}
	if len(problems) > 0 {
		flowValidationErrors(c, problems)
		return
	}
	channels, err := q.ListMarketingChannels(c, row.TenantID)
	if flowFailure(c, err) {
		return
	}
	available := false
	for _, ch := range channels {
		available = available || ch.Enabled && ch.Kind == "dedicated_gateway"
	}
	if !available {
		authError(c, 409, "MARKETING_UNAVAILABLE")
		return
	}
	runtimeID, err := q.CreateMarketingRuntimeFlow(c, store.CreateMarketingRuntimeFlowParams{TenantID: row.TenantID, Name: row.Name})
	if flowFailure(c, err) {
		return
	}
	for i, n := range d.Nodes {
		err = q.CreateMarketingRuntimeStep(c, store.CreateMarketingRuntimeStepParams{TenantID: row.TenantID, FlowID: runtimeID, StepKey: n.ID, Kind: n.Kind, NextKey: n.Next, OpenedKey: n.Otherwise, Position: int32(i), // #nosec G115 -- graph validation limits this index to nineteen.
			WaitSeconds: n.WaitSeconds, ConditionField: n.Field, ConditionOperator: n.Operator, ConditionValue: n.Value})
		if flowFailure(c, err) {
			return
		}
		if n.Kind == "send" {
			content := templates[n.ID]
			err = q.CreateMarketingFlowContentSnapshot(c, store.CreateMarketingFlowContentSnapshotParams{TenantID: row.TenantID, RuntimeFlowID: runtimeID, StepKey: n.ID, TemplateID: n.TemplateID, Subject: content.Subject, TextBody: content.TextBody})
			if flowFailure(c, err) {
				return
			}
		}
	}
	actor, _ := middleware.GetUserID(c)
	version, err := q.CreateMarketingFlowVersion(c, store.CreateMarketingFlowVersionParams{TenantID: row.TenantID, DefinitionID: row.ID, Version: row.PublishedVersion + 1, DraftRevision: row.DraftRevision, RuntimeFlowID: runtimeID, Definition: row.Draft, PublishedBy: actor})
	if flowFailure(c, err) {
		return
	}
	if err = q.PublishMarketingFlowDefinition(c, store.PublishMarketingFlowDefinitionParams{TenantID: row.TenantID, ID: row.ID, PublishedVersion: version.Version}); flowFailure(c, err) {
		return
	}
	if !h.audit(c, q, row, "published", gin.H{"version": version.Version, "draft_revision": row.DraftRevision}) {
		return
	}
	c.JSON(201, gin.H{"version": version.Version, "duplicate": false})
}
func (h *marketingFlowHandler) deactivate(c *gin.Context) {
	q, row, ok := h.definition(c)
	if !ok {
		return
	}
	if err := q.PauseMarketingFlowDefinition(c, store.PauseMarketingFlowDefinitionParams{TenantID: row.TenantID, ID: row.ID}); flowFailure(c, err) {
		return
	}
	if err := q.PauseMarketingRuntimeFlows(c, store.PauseMarketingRuntimeFlowsParams{TenantID: row.TenantID, DefinitionID: row.ID}); flowFailure(c, err) {
		return
	}
	if !h.audit(c, q, row, "deactivated", gin.H{"policy": "pause_all_existing_versions"}) {
		return
	}
	c.JSON(200, gin.H{"status": "paused", "existing_instances": "paused_at_current_node", "already_submitted": "cannot_recall"})
}
func flowCursor(c *gin.Context) (int64, bool) {
	raw := c.Query("before")
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		authError(c, 400, "INVALID_PARAMS")
		return 0, false
	}
	return v, true
}
func (h *marketingFlowHandler) versions(c *gin.Context) {
	q, row, ok := h.definition(c)
	if !ok {
		return
	}
	before, ok := flowCursor(c)
	if !ok {
		return
	}
	rows, err := q.ListMarketingFlowVersions(c, store.ListMarketingFlowVersionsParams{TenantID: row.TenantID, DefinitionID: row.ID, BeforeID: before})
	if flowFailure(c, err) {
		return
	}
	next := int64(0)
	if len(rows) > 50 {
		rows = rows[:50]
		next = rows[49].ID
	}
	c.JSON(200, gin.H{"versions": rows, "next_cursor": next})
}

var flowEventKey = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`)

func (h *marketingFlowHandler) enroll(c *gin.Context) {
	q, row, ok := h.definition(c)
	if !ok {
		return
	}
	if !h.workerEnabled {
		authError(c, 503, "FLOW_WORKER_DISABLED")
		return
	}
	var input struct {
		EventKey string `json:"event_key"`
		ListID   int64  `json:"list_id"`
		Source   string `json:"source"`
	}
	if !decodeMarketingFlow(c, &input) {
		return
	}
	if !flowEventKey.MatchString(input.EventKey) || input.ListID <= 0 || input.Source != "contact_enrolled" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	existing, err := q.GetMarketingFlowEntry(c, store.GetMarketingFlowEntryParams{TenantID: row.TenantID, DefinitionID: row.ID, EventKey: input.EventKey})
	if err == nil {
		if existing.ListID != input.ListID {
			authError(c, 409, "FLOW_EVENT_CONFLICT")
			return
		}
		c.JSON(200, gin.H{"entry_id": existing.ID, "enrolled_count": existing.RecipientCount, "duplicate": true})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		flowFailure(c, err)
		return
	}
	if row.Status != "active" || row.PublishedVersion < 1 {
		authError(c, 409, "FLOW_NOT_ACTIVE")
		return
	}
	version, err := q.GetMarketingFlowVersion(c, store.GetMarketingFlowVersionParams{TenantID: row.TenantID, DefinitionID: row.ID, Version: row.PublishedVersion})
	if flowFailure(c, err) {
		return
	}
	if _, err = q.GetMarketingFlowList(c, store.GetMarketingFlowListParams{TenantID: row.TenantID, ID: input.ListID}); flowFailure(c, err) {
		return
	}
	contacts, err := q.ListMarketingFlowEligibleContacts(c, store.ListMarketingFlowEligibleContactsParams{TenantID: row.TenantID, ListID: input.ListID, FlowID: version.RuntimeFlowID})
	if flowFailure(c, err) {
		return
	}
	count, err := q.CountMarketingFlowLiveInstances(c, row.TenantID)
	if flowFailure(c, err) {
		return
	}
	if len(contacts) > 1000 || count+int64(len(contacts)) > 10000 {
		authError(c, 409, "FLOW_BUDGET_EXCEEDED")
		return
	}
	recipientCount := int32(len(contacts)) // #nosec G115 -- the enrollment guard above limits this slice to 1000.
	entry, err := q.CreateMarketingFlowEntry(c, store.CreateMarketingFlowEntryParams{TenantID: row.TenantID, DefinitionID: row.ID, VersionID: version.ID, EventKey: input.EventKey, ListID: input.ListID, RecipientCount: recipientCount})
	if flowFailure(c, err) {
		return
	}
	var d flow.Definition
	if json.Unmarshal(version.Definition, &d) != nil {
		authError(c, 500, "DB_ERROR")
		return
	}
	for _, address := range contacts {
		domainIndex := strings.LastIndex(address, "@")
		if domainIndex < 1 {
			authError(c, 500, "DB_ERROR")
			return
		}
		facts, _ := json.Marshal(map[string]string{"source": "contact_enrolled", "email_domain": strings.ToLower(address[domainIndex+1:]), "list_id": strconv.FormatInt(input.ListID, 10), "consented": "true"})
		if _, err = q.CreateMarketingFlowInstance(c, store.CreateMarketingFlowInstanceParams{TenantID: row.TenantID, FlowID: version.RuntimeFlowID, Email: address, StepKey: d.Entry, EntryID: pgtype.Int8{Int64: entry.ID, Valid: true}, EventFacts: facts}); flowFailure(c, err) {
			return
		}
	}
	if !h.audit(c, q, row, "enrolled", gin.H{"entry_id": entry.ID, "version": version.Version, "recipient_count": len(contacts)}) {
		return
	}
	c.JSON(201, gin.H{"entry_id": entry.ID, "enrolled_count": len(contacts), "duplicate": false})
}
func (h *marketingFlowHandler) instances(c *gin.Context) {
	q, row, ok := h.definition(c)
	if !ok {
		return
	}
	before, ok := flowCursor(c)
	if !ok {
		return
	}
	email := strings.TrimSpace(c.Query("email"))
	if len(email) > 254 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	rows, err := q.ListMarketingFlowInstances(c, store.ListMarketingFlowInstancesParams{TenantID: row.TenantID, DefinitionID: row.ID, BeforeID: before, Email: email})
	if flowFailure(c, err) {
		return
	}
	next := int64(0)
	if len(rows) > 100 {
		rows = rows[:100]
		next = rows[99].ID
	}
	instances := make([]gin.H, 0, len(rows))
	for _, p := range rows {
		state := "pending"
		switch {
		case p.StoppedAt.Valid:
			state = "stopped"
		case p.Unsubscribed:
			state = "unsubscribed"
		case p.DeliveryUncertain:
			state = "uncertain"
			if p.LeaseUntil.Valid && p.LeaseUntil.Time.After(time.Now()) {
				state = "sending"
			}
		case p.Kind == "stop":
			state = "completed"
		case p.FlowStatus != "active":
			state = "paused"
		case p.LastError != "":
			state = "failed"
		case p.WaitUntil.Valid && p.WaitUntil.Time.After(time.Now()):
			state = "waiting"
		}
		instances = append(instances, gin.H{"id": p.ID, "email": p.Email, "node": p.StepKey, "version": p.Version, "state": state, "error_code": p.LastError, "wait_until": p.WaitUntil, "next_attempt_at": p.NextAttemptAt, "attempt_count": p.AttemptCount, "delivery_uncertain": p.DeliveryUncertain, "updated_at": p.UpdatedAt})
	}
	c.JSON(200, gin.H{"instances": instances, "next_cursor": next})
}
func (h *marketingFlowHandler) executions(c *gin.Context) {
	q, row, ok := h.definition(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "instance_id")
	if !ok {
		return
	}
	if _, err := q.GetMarketingFlowInstance(c, store.GetMarketingFlowInstanceParams{TenantID: row.TenantID, DefinitionID: row.ID, ID: id}); flowFailure(c, err) {
		return
	}
	rows, err := q.ListMarketingFlowNodeExecutions(c, store.ListMarketingFlowNodeExecutionsParams{TenantID: row.TenantID, DefinitionID: row.ID, PositionID: id})
	if flowFailure(c, err) {
		return
	}
	c.JSON(200, gin.H{"executions": rows})
}
func (h *marketingFlowHandler) retry(c *gin.Context) {
	q, row, ok := h.definition(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "instance_id")
	if !ok {
		return
	}
	var input struct {
		Reason               string `json:"reason"`
		ConfirmedNotAccepted bool   `json:"confirmed_not_accepted"`
	}
	if !decodeMarketingFlow(c, &input) {
		return
	}
	if len(strings.TrimSpace(input.Reason)) < 10 || len(input.Reason) > 2000 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	retried, err := q.RetryMarketingFlowInstance(c, store.RetryMarketingFlowInstanceParams{PTenant: row.TenantID, PDefinition: row.ID, PPosition: id, PConfirmedNotAccepted: input.ConfirmedNotAccepted})
	if flowFailure(c, err) {
		return
	}
	if !retried {
		authError(c, 409, "FLOW_RETRY_NOT_ALLOWED")
		return
	}
	if !h.audit(c, q, row, "retry_requested", gin.H{"instance_id": id, "reason": input.Reason, "confirmed_not_accepted": input.ConfirmedNotAccepted}) {
		return
	}
	c.JSON(200, gin.H{"retried": true})
}
func (h *marketingFlowHandler) stop(c *gin.Context) {
	q, row, ok := h.definition(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "instance_id")
	if !ok {
		return
	}
	count, err := q.StopMarketingFlowInstance(c, store.StopMarketingFlowInstanceParams{TenantID: row.TenantID, DefinitionID: row.ID, ID: id})
	if flowFailure(c, err) {
		return
	}
	if count == 0 {
		authError(c, 404, "NOT_FOUND")
		return
	}
	if !h.audit(c, q, row, "instance_stopped", gin.H{"instance_id": id}) {
		return
	}
	c.JSON(200, gin.H{"stopped": true, "already_submitted": "cannot_recall"})
}
func (h *marketingFlowHandler) audit(c *gin.Context, q *store.Queries, row store.MarketingFlowDefinitions, action string, detail gin.H) bool {
	encoded, _ := json.Marshal(detail)
	err := q.RecordMarketingFlowAudit(c, store.RecordMarketingFlowAuditParams{PTenant: row.TenantID, PDefinition: row.ID, PAction: action, PDetail: encoded})
	return !flowFailure(c, err)
}

func flowValidationErrors(c *gin.Context, problems []flow.NodeError) {
	c.JSON(422, gin.H{"error": gin.H{"code": "FLOW_VALIDATION_FAILED", "params": gin.H{"nodes": problems}}, "errors": problems})
}

func (h *marketingFlowHandler) version(c *gin.Context) {
	q, row, ok := h.definition(c)
	if !ok {
		return
	}
	number, err := strconv.ParseInt(c.Param("version"), 10, 32)
	if err != nil || number < 1 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	version, err := q.GetMarketingFlowVersion(c, store.GetMarketingFlowVersionParams{TenantID: row.TenantID, DefinitionID: row.ID, Version: int32(number)})
	if flowFailure(c, err) {
		return
	}
	templates, err := q.ListMarketingFlowPublishedContent(c, store.ListMarketingFlowPublishedContentParams{TenantID: row.TenantID, RuntimeFlowID: version.RuntimeFlowID})
	if flowFailure(c, err) {
		return
	}
	c.JSON(200, gin.H{"version": version.Version, "draft_revision": version.DraftRevision, "definition": json.RawMessage(version.Definition), "templates": templates})
}
