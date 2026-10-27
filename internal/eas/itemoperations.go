package eas

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bushixiucai/urmail/internal/webmail"
)

const pageItemOperations = byte(20)
const tokItemOperations = byte(0x05)
const tokIOFetch = byte(0x06)
const tokIOStore = byte(0x07)
const tokIOOptions = byte(0x08)
const tokIORange = byte(0x09)
const tokIOTotal = byte(0x0A)
const tokIOProperties = byte(0x0B)
const tokIOData = byte(0x0C)
const tokIOStatus = byte(0x0D)
const tokIOResponse = byte(0x0E)
const maxFetchBytes = 20 << 20

// File references are opaque identifiers, never authorization credentials.
// Every component is checked against the authenticated principal, live restore
// generation, device sync snapshot and current IMAP UIDVALIDITY on every fetch.
type fileReference struct {
	Tenant      int64  `json:"t"`
	Mailbox     int64  `json:"m"`
	Device      string `json:"d"`
	Folder      string `json:"f"`
	UID         uint32 `json:"u"`
	UIDValidity uint32 `json:"v"`
	Epoch       int64  `json:"e"`
	Part        string `json:"p"`
}

func encodeFileReference(ref fileReference) string {
	raw, _ := json.Marshal(ref)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func attachmentElements(sess Session, device, folder string, uidv uint32, epoch int64, msg *webmail.Message) *Node {
	all := Element(pageAirSyncBase, 0x0E)
	for _, att := range msg.Attachments {
		ref := encodeFileReference(fileReference{Tenant: sess.TenantID, Mailbox: sess.MailboxID, Device: device, Folder: folder, UID: msg.UID, UIDValidity: uidv, Epoch: epoch, Part: att.PartID})
		n := Element(pageAirSyncBase, 0x0F, Text(pageAirSyncBase, 0x10, att.Filename), Text(pageAirSyncBase, 0x11, ref), Text(pageAirSyncBase, 0x12, "1"), Text(pageAirSyncBase, 0x0C, strconv.FormatInt(att.Size, 10)))
		if att.ContentID != "" {
			n.Add(Text(pageAirSyncBase, 0x13, att.ContentID))
			n.Add(Text(pageAirSyncBase, 0x15, "1"))
		}
		all.Add(n)
	}
	return all
}

func nodeOnPage(n *Node, page, token byte) *Node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Page == page && c.Token == token {
			return c
		}
	}
	return nil
}
func pageText(n *Node, page, token byte) string {
	c := nodeOnPage(n, page, token)
	if c == nil {
		return ""
	}
	return c.Text
}

func (s *Server) handleItemOperations(ctx context.Context, sess Session, device string, req *Node) (*Node, error) {
	root := Element(pageItemOperations, tokItemOperations)
	if req.Page != pageItemOperations || req.Token != tokItemOperations || len(req.Children) == 0 || len(req.Children) > 10 {
		return root.Add(Text(pageItemOperations, tokIOStatus, "2")), nil
	}
	if s.Dialer == nil || s.Vault == nil {
		return root.Add(Text(pageItemOperations, tokIOStatus, "3")), nil
	}
	secret, ok := s.Vault.Get(sess.TokenHash)
	if !ok {
		return root.Add(Text(pageItemOperations, tokIOStatus, "18")), nil
	}
	client, err := s.Dialer.Dial(ctx, sess.Address, secret)
	if err != nil {
		return root.Add(Text(pageItemOperations, tokIOStatus, "3")), nil
	}
	defer func() { _ = client.Close() }()
	root.Add(Text(pageItemOperations, tokIOStatus, "1"))
	response := Element(pageItemOperations, tokIOResponse)
	remaining := maxFetchBytes
	for _, fetch := range req.Children {
		if fetch.Page != pageItemOperations || fetch.Token != tokIOFetch {
			response.Add(Element(pageItemOperations, fetch.Token, Text(pageItemOperations, tokIOStatus, "2")))
			continue
		}
		item := s.fetchMailItem(ctx, sess, device, client, fetch, remaining)
		remaining -= len(Encode(item))
		response.Add(item)
	}
	return root.Add(response), nil
}

func (s *Server) fetchMailItem(ctx context.Context, sess Session, device string, client webmail.Client, fetch *Node, budget int) *Node {
	resp := Element(pageItemOperations, tokIOFetch)
	fail := func(status string) *Node {
		resp.Children = append([]*Node{Text(pageItemOperations, tokIOStatus, status)}, resp.Children...)
		return resp
	}
	if pageText(fetch, pageItemOperations, tokIOStore) != "Mailbox" {
		return fail("9")
	}
	folderID, itemID := pageText(fetch, pageAirSync, tokCollectionID), pageText(fetch, pageAirSync, tokServerID)
	encodedRef := pageText(fetch, pageAirSyncBase, 0x11)
	var ref fileReference
	if encodedRef != "" {
		if len(encodedRef) > 4096 {
			return fail("15")
		}
		raw, err := base64.RawURLEncoding.DecodeString(encodedRef)
		if err != nil || json.Unmarshal(raw, &ref) != nil || ref.Tenant != sess.TenantID || ref.Mailbox != sess.MailboxID || ref.Device != device || ref.Part == "" {
			return fail("15")
		}
		folderID, itemID = ref.Folder, strconv.FormatUint(uint64(ref.UID), 10)
		resp.Add(Text(pageAirSyncBase, 0x11, encodedRef))
	} else {
		resp.Add(Text(pageAirSync, tokCollectionID, folderID))
		resp.Add(Text(pageAirSync, tokServerID, itemID))
	}
	if !strings.HasPrefix(folderID, "e:") || !validUID(itemID) {
		return fail("6")
	}
	state, err := loadCollectionState(ctx, s.Pool, sess, device, folderID)
	if err != nil {
		return fail("6")
	}
	epoch := state.Epoch
	items, err := loadItems(ctx, s.Pool, sess, device, folderID)
	if err != nil {
		return fail("3")
	}
	if _, known := items[itemID]; !known {
		return fail("6")
	}
	folder := strings.TrimPrefix(folderID, "e:")
	uid := parseUID(itemID)
	uidv, err := client.FolderUIDValidity(ctx, folder)
	if err != nil {
		return fail("3")
	}
	if int64(uidv) != state.UIDValidity || (encodedRef != "" && (ref.Epoch != epoch || ref.UIDValidity != uidv)) {
		return fail("6")
	}
	options := nodeOnPage(fetch, pageItemOperations, tokIOOptions)
	props := Element(pageItemOperations, tokIOProperties)
	if encodedRef != "" {
		_, ct, data, e := client.GetAttachment(ctx, folder, uid, ref.Part)
		if errors.Is(e, webmail.ErrAttachmentTooLarge) {
			return fail("11")
		}
		if e != nil {
			return fail("15")
		}
		total := len(data)
		if total > webmail.MaxAttachmentBytes {
			return fail("11")
		}
		if value := pageText(options, pageItemOperations, tokIORange); value != "" {
			start, end, valid := fetchRange(value, total)
			if !valid {
				return fail("8")
			}
			data = data[start : end+1]
			props.Add(Text(pageItemOperations, tokIORange, strconv.Itoa(start)+"-"+strconv.Itoa(end)))
		}
		if len(data) > budget-4096 {
			return fail("11")
		}
		props.Add(Text(pageAirSyncBase, 0x17, ct))
		props.Add(Text(pageItemOperations, tokIOTotal, strconv.Itoa(total)))
		props.Add(OpaqueNode(pageItemOperations, tokIOData, data))
	} else {
		msg, e := client.GetMessage(ctx, folder, uid)
		if e != nil || msg == nil {
			return fail("6")
		}
		if msg.UIDValidity != 0 && msg.UIDValidity != uidv {
			return fail("6")
		}
		if msg.AttachmentsCapped {
			return fail("11")
		}
		bodyType, limit, allOrNone := bodyPreference(options)
		var data []byte
		switch bodyType {
		case "1":
			data = []byte(msg.Text)
		case "2":
			data = []byte(msg.HTML)
			if len(data) == 0 {
				bodyType = "1"
				data = []byte(msg.Text)
			}
		case "4":
			if pageText(options, pageAirSync, 0x22) != "2" {
				return fail("2")
			}
			reader, ok := client.(webmail.RawMessageAccessor)
			if !ok {
				return fail("14")
			}
			var rawUIDV uint32
			data, rawUIDV, e = reader.GetRawMessage(ctx, folder, uid)
			if rawUIDV != uidv {
				return fail("6")
			}
			if e != nil {
				return fail("14")
			}
		default:
			return fail("2")
		}
		total := len(data)
		truncated := "0"
		ceiling := budget - 16384
		if ceiling < 0 {
			return fail("11")
		}
		if limit < 0 || limit > ceiling {
			limit = ceiling
		}
		if len(data) > limit {
			if allOrNone {
				return fail("11")
			}
			data = data[:limit]
			if bodyType != "4" {
				for !utf8.Valid(data) && len(data) > 0 {
					data = data[:len(data)-1]
				}
			}
			truncated = "1"
		}
		body := Element(pageAirSyncBase, tokBaseBody, Text(pageAirSyncBase, tokBaseType, bodyType), Text(pageAirSyncBase, tokBaseEstimatedDataSize, strconv.Itoa(total)), Text(pageAirSyncBase, tokBaseTruncated, truncated))
		if bodyType == "4" {
			body.Add(OpaqueNode(pageAirSyncBase, tokBaseData, data))
		} else {
			body.Add(Text(pageAirSyncBase, tokBaseData, string(data)))
		}
		props.Add(Text(pageEmail, tokEmailSubject, msg.Subject))
		nativeType := "1"
		if msg.HTML != "" {
			nativeType = "2"
		}
		props.Add(Text(pageAirSyncBase, 0x16, nativeType))
		props.Add(body)
		if len(msg.Attachments) > 0 {
			props.Add(attachmentElements(sess, device, folderID, uidv, epoch, msg))
		}
		resp.Add(Text(pageAirSync, tokClass, "Email"))
	}
	resp.Children = append([]*Node{Text(pageItemOperations, tokIOStatus, "1")}, resp.Children...)
	resp.Add(props)
	if len(Encode(resp)) > budget {
		return Element(pageItemOperations, tokIOFetch, Text(pageItemOperations, tokIOStatus, "11"))
	}
	return resp
}

func bodyPreference(options *Node) (string, int, bool) {
	for _, pref := range func() []*Node {
		if options == nil {
			return nil
		}
		return options.Children
	}() {
		if pref.Page != pageAirSyncBase || pref.Token != 0x05 {
			continue
		}
		typ := pageText(pref, pageAirSyncBase, tokBaseType)
		if typ != "1" && typ != "2" && typ != "4" {
			continue
		}
		limit := -1
		if value := pageText(pref, pageAirSyncBase, 0x07); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				return "invalid", 0, false
			}
			limit = parsed
		}
		return typ, limit, pageText(pref, pageAirSyncBase, 0x08) == "1"
	}
	return "1", -1, false
}
func fetchRange(value string, total int) (int, int, bool) {
	a, b, ok := strings.Cut(value, "-")
	if !ok || total == 0 {
		return 0, 0, false
	}
	start, e := strconv.Atoi(a)
	if e != nil || start < 0 || start >= total {
		return 0, 0, false
	}
	end := total - 1
	if b != "" {
		end, e = strconv.Atoi(b)
		if e != nil || end < start {
			return 0, 0, false
		}
		if end >= total {
			end = total - 1
		}
	}
	return start, end, true
}
