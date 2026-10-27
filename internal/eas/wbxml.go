// Package eas implements the Exchange ActiveSync transport layer (Task EAS-002):
// WBXML codec per MS-ASWBXML, version negotiation per MS-ASHTTP, device
// authentication boundary, and the Provision handshake, FolderSync and Ping
// commands. Data sync (EAS-003) stays behind the SyncHandler interface.
package eas

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// WBXML token values shared by all code pages.
const (
	tokenEnd        = 0x01
	tokenInlineStr  = 0x03
	tokenOpaque     = 0xC3
	tokenPageSwitch = 0x00
	contentFlag     = 0x40
	wbxmlVersion13  = 0x03
)

// Node is one decoded WBXML element. Content is the inline string text; Opaque
// carries binary content. Children are ordered as decoded.
type Node struct {
	Page     byte
	Token    byte
	Text     string
	Opaque   []byte
	Children []*Node
}

// Name returns a human readable page:token identifier used in errors.
func (n *Node) Name() string { return fmt.Sprintf("%d:%02x", n.Page, n.Token) }

// Add returns n with child appended, for fluent request building.
func (n *Node) Add(child *Node) *Node {
	n.Children = append(n.Children, child)
	return n
}

// Text child convenience constructor.
func Text(page, token byte, text string) *Node {
	return &Node{Page: page, Token: token, Text: text}
}

// Element convenience constructor for a node without content.
func Element(page, token byte, children ...*Node) *Node {
	return &Node{Page: page, Token: token, Children: children}
}

// OpaqueNode is the opaque (binary) child convenience constructor.
func OpaqueNode(page, token byte, data []byte) *Node {
	return &Node{Page: page, Token: token, Opaque: data}
}

// codePages lists the WBXML code pages implemented per MS-ASWBXML. The
// standard assigns: 0 AirSync, 6 GetItemEstimate, 7 FolderHierarchy,
// 13 Ping, 14 Provision, 17 AirSyncBase. GetItemEstimate is deliberately not
// implemented as a command (see EAS_EVIDENCE.md section 2); its page is
// registered so decoders can reject tokens deterministically. Pages 1, 2
// and 4 carry the EAS-003 application data classes.
var codePages = map[byte]string{
	0:  "AirSync",
	1:  "Contacts",
	2:  "Email",
	4:  "Calendar",
	6:  "GetItemEstimate",
	7:  "FolderHierarchy",
	13: "Ping",
	14: "Provision",
	17: "AirSyncBase",
	18: "Settings",
	20: "ItemOperations",
	21: "ComposeMail",
}

// tokens maps code page -> token byte -> element name, per the MS-ASWBXML
// code page tables. Only pages in codePages are present.
var tokens = map[byte]map[byte]string{
	0:  {0x05: "Sync", 0x06: "Responses", 0x07: "Add", 0x08: "Change", 0x09: "Delete", 0x0A: "Fetch", 0x0B: "SyncKey", 0x0C: "ClientId", 0x0D: "ServerId", 0x0E: "Status", 0x0F: "Collection", 0x10: "Class", 0x12: "CollectionId", 0x13: "GetChanges", 0x14: "MoreAvailable", 0x15: "WindowSize", 0x16: "Commands", 0x17: "Options", 0x18: "FilterType", 0x1B: "Conflict", 0x1C: "Collections", 0x1D: "ApplicationData", 0x1E: "DeletesAsMoves", 0x20: "Supported", 0x21: "SoftDelete", 0x22: "MIMESupport", 0x23: "MIMETruncation", 0x24: "Wait", 0x25: "Limit", 0x26: "Partial", 0x27: "ConversationMode", 0x28: "MaxItems", 0x29: "HeartbeatInterval"},
	1:  {0x05: "Anniversary", 0x06: "AssistantName", 0x07: "AssistantPhoneNumber", 0x08: "Birthday", 0x0C: "Business2PhoneNumber", 0x0D: "BusinessAddressCity", 0x0E: "BusinessAddressCountry", 0x0F: "BusinessAddressPostalCode", 0x10: "BusinessAddressState", 0x11: "BusinessAddressStreet", 0x12: "BusinessFaxNumber", 0x13: "BusinessPhoneNumber", 0x14: "CarPhoneNumber", 0x15: "Categories", 0x16: "Category", 0x17: "Children", 0x18: "Child", 0x19: "CompanyName", 0x1A: "Department", 0x1B: "Email1Address", 0x1C: "Email2Address", 0x1D: "Email3Address", 0x1E: "FileAs", 0x1F: "FirstName", 0x20: "Home2PhoneNumber", 0x21: "HomeAddressCity", 0x22: "HomeAddressCountry", 0x23: "HomeAddressPostalCode", 0x24: "HomeAddressState", 0x25: "HomeAddressStreet", 0x26: "HomeFaxNumber", 0x27: "HomePhoneNumber", 0x28: "JobTitle", 0x29: "LastName", 0x2A: "MiddleName", 0x2B: "MobilePhoneNumber", 0x2C: "OfficeLocation", 0x2D: "OtherAddressCity", 0x2E: "OtherAddressCountry", 0x2F: "OtherAddressPostalCode", 0x30: "OtherAddressState", 0x31: "OtherAddressStreet", 0x32: "PagerNumber", 0x33: "RadioPhoneNumber", 0x34: "Spouse", 0x35: "Suffix", 0x36: "Title", 0x37: "WebPage", 0x38: "YomiCompanyName", 0x39: "YomiFirstName", 0x3A: "YomiLastName", 0x3C: "Picture", 0x3D: "Alias", 0x3E: "WeightedRank"},
	2:  {0x0F: "DateReceived", 0x11: "DisplayTo", 0x12: "Importance", 0x13: "MessageClass", 0x14: "Subject", 0x15: "Read", 0x16: "To", 0x17: "Cc", 0x18: "From", 0x19: "ReplyTo", 0x1A: "AllDayEvent", 0x1B: "Categories", 0x1C: "Category", 0x1D: "DtStamp", 0x1E: "EndTime", 0x1F: "InstanceType", 0x20: "BusyStatus", 0x21: "Location", 0x22: "MeetingRequest", 0x23: "Organizer", 0x24: "RecurrenceId", 0x25: "Reminder", 0x26: "ResponseRequested", 0x27: "Recurrences", 0x28: "Recurrence", 0x29: "Type", 0x2A: "Until", 0x2B: "Occurrences", 0x2C: "Interval", 0x2D: "DayOfWeek", 0x2E: "DayOfMonth", 0x2F: "WeekOfMonth", 0x30: "MonthOfYear", 0x31: "StartTime", 0x32: "Sensitivity", 0x33: "TimeZone", 0x34: "GlobalObjId", 0x35: "ThreadTopic", 0x39: "InternetCPID", 0x3A: "Flag", 0x3B: "Status", 0x3C: "ContentClass", 0x3D: "FlagType", 0x3E: "CompleteTime", 0x3F: "DisallowNewTimeProposal"},
	4:  {0x05: "TimeZone", 0x06: "AllDayEvent", 0x07: "Attendees", 0x08: "Attendee", 0x09: "Email", 0x0A: "Name", 0x0D: "BusyStatus", 0x0E: "Categories", 0x0F: "Category", 0x11: "DtStamp", 0x12: "EndTime", 0x13: "Exception", 0x14: "Exceptions", 0x15: "Deleted", 0x16: "ExceptionStartTime", 0x17: "Location", 0x18: "MeetingStatus", 0x19: "OrganizerEmail", 0x1A: "OrganizerName", 0x1B: "Recurrence", 0x1C: "Type", 0x1D: "Until", 0x1E: "Occurrences", 0x1F: "Interval", 0x20: "DayOfWeek", 0x21: "DayOfMonth", 0x22: "WeekOfMonth", 0x23: "MonthOfYear", 0x24: "Reminder", 0x25: "Sensitivity", 0x26: "Subject", 0x27: "StartTime", 0x28: "UID", 0x29: "AttendeeStatus", 0x2A: "AttendeeType", 0x33: "DisallowNewTimeProposal", 0x34: "ResponseRequested", 0x35: "AppointmentReplyTime", 0x36: "ResponseType", 0x37: "CalendarType", 0x38: "IsLeapMonth", 0x39: "FirstDayOfWeek", 0x3A: "OnlineMeetingConfLink", 0x3B: "OnlineMeetingExternalLink"},
	6:  {0x05: "GetItemEstimate", 0x06: "Version", 0x07: "Collections", 0x08: "Collection", 0x09: "Class", 0x0A: "CollectionId", 0x0B: "DateTime", 0x0C: "Estimate", 0x0D: "Response", 0x0E: "Status"},
	7:  {0x07: "DisplayName", 0x08: "ServerId", 0x09: "ParentId", 0x0A: "Type", 0x0C: "Status", 0x0E: "Changes", 0x0F: "Add", 0x10: "Delete", 0x11: "Update", 0x12: "SyncKey", 0x13: "FolderCreate", 0x14: "FolderDelete", 0x15: "FolderUpdate", 0x16: "FolderSync", 0x17: "Count"},
	13: {0x05: "Ping", 0x06: "AutdState", 0x07: "Status", 0x08: "HeartbeatInterval", 0x09: "Folders", 0x0A: "Folder", 0x0B: "Id", 0x0C: "Class", 0x0D: "MaxFolders"},
	14: {0x05: "Provision", 0x06: "Policies", 0x07: "Policy", 0x08: "PolicyType", 0x09: "PolicyKey", 0x0A: "Data", 0x0B: "Status", 0x0C: "RemoteWipe", 0x0D: "EASProvisionDoc", 0x0E: "DevicePasswordEnabled", 0x0F: "AlphanumericDevicePasswordRequired", 0x10: "RequireStorageCardEncryption", 0x11: "PasswordRecoveryEnabled", 0x13: "AttachmentsEnabled", 0x14: "MinDevicePasswordLength", 0x15: "MaxInactivityTimeDeviceLock", 0x16: "MaxDevicePasswordFailedAttempts", 0x17: "MaxAttachmentSize", 0x18: "AllowSimpleDevicePassword", 0x19: "DevicePasswordExpiration", 0x1A: "DevicePasswordHistory", 0x1B: "AllowStorageCard", 0x1C: "AllowCamera", 0x1D: "RequireDeviceEncryption", 0x1E: "AllowUnsignedApplications", 0x1F: "AllowUnsignedInstallationPackages", 0x20: "MinDevicePasswordComplexCharacters", 0x21: "AllowWiFi", 0x22: "AllowTextMessaging", 0x23: "AllowPOPIMAPEmail", 0x24: "AllowBluetooth", 0x25: "AllowIrDA", 0x26: "RequireManualSyncWhenRoaming", 0x27: "AllowDesktopSync", 0x28: "MaxCalendarAgeFilter", 0x29: "AllowHTMLEmail", 0x2A: "MaxEmailAgeFilter", 0x2B: "MaxEmailBodyTruncationSize", 0x2C: "MaxEmailHTMLBodyTruncationSize", 0x2D: "RequireSignedSMIMEMessages", 0x2E: "RequireEncryptedSMIMEMessages", 0x2F: "RequireSignedSMIMEAlgorithm", 0x30: "RequireEncryptionSMIMEAlgorithm", 0x31: "AllowSMIMEEncryptionAlgorithmNegotiation", 0x32: "AllowSMIMESoftCerts", 0x33: "AllowBrowser", 0x34: "AllowConsumerEmail", 0x35: "AllowRemoteDesktop", 0x36: "AllowInternetSharing", 0x37: "UnapprovedInROMApplicationList", 0x38: "ApplicationName", 0x39: "ApprovedApplicationList", 0x3A: "Hash"},
	17: {0x05: "BodyPreference", 0x06: "Type", 0x07: "TruncationSize", 0x08: "AllOrNone", 0x0A: "Body", 0x0B: "Data", 0x0C: "EstimatedDataSize", 0x0D: "Truncated", 0x0E: "Attachments", 0x0F: "Attachment", 0x10: "DisplayName", 0x11: "FileReference", 0x12: "Method", 0x13: "ContentId", 0x14: "ContentLocation", 0x15: "IsInline", 0x16: "NativeBodyType", 0x17: "ContentType", 0x18: "Preview", 0x19: "BodyPartPreference", 0x1A: "BodyPart", 0x1B: "Status"},
	18: {0x05: "Settings", 0x06: "Status", 0x07: "Get", 0x08: "Set", 0x09: "Oof", 0x0A: "OofState", 0x0B: "StartTime", 0x0C: "EndTime", 0x0D: "OofMessage", 0x0E: "AppliesToInternal", 0x0F: "AppliesToExternalKnown", 0x10: "AppliesToExternalUnknown", 0x11: "Enabled", 0x12: "ReplyMessage", 0x13: "BodyType", 0x14: "DevicePassword", 0x15: "Password", 0x16: "DeviceInformation", 0x17: "Model", 0x18: "IMEI", 0x19: "FriendlyName", 0x1A: "OS", 0x1B: "OSLanguage", 0x1C: "PhoneNumber", 0x1D: "UserInformation", 0x1E: "EmailAddresses", 0x1F: "SMTPAddress", 0x20: "UserAgent", 0x21: "EnableOutboundSMS", 0x22: "MobileOperator", 0x23: "PrimarySmtpAddress", 0x24: "Accounts", 0x25: "Account", 0x26: "AccountId", 0x27: "AccountName", 0x28: "UserDisplayName", 0x29: "SendDisabled", 0x2B: "RightsManagementInformation"},
	20: {0x05: "ItemOperations", 0x06: "Fetch", 0x07: "Store", 0x08: "Options", 0x09: "Range", 0x0A: "Total", 0x0B: "Properties", 0x0C: "Data", 0x0D: "Status", 0x0E: "Response", 0x0F: "Version", 0x10: "Schema", 0x11: "Part", 0x12: "EmptyFolderContents", 0x13: "DeleteSubFolders", 0x14: "UserName", 0x15: "Password", 0x16: "Move", 0x17: "DstFldId", 0x18: "ConversationId", 0x19: "MoveAlways"},
	21: {0x05: "SendMail", 0x06: "SmartForward", 0x07: "SmartReply", 0x08: "SaveInSentItems", 0x09: "ReplaceMime", 0x0B: "Source", 0x0C: "FolderId", 0x0D: "ItemId", 0x0E: "LongId", 0x0F: "InstanceId", 0x10: "Mime", 0x11: "ClientId", 0x12: "Status", 0x13: "AccountId"},
}

// TokenName resolves the element name for diagnostics.
func TokenName(page, token byte) string {
	if names, ok := tokens[page]; ok {
		if name, ok := names[token&^contentFlag]; ok {
			return name
		}
	}
	return fmt.Sprintf("unknown(%d:%02x)", page, token)
}

// Encode serializes root as one WBXML document (WBXML 1.3, UTF-8, empty
// string table) exactly as MS-ASWBXML section 2.2 defines.
func Encode(root *Node) []byte {
	out := []byte{wbxmlVersion13, 0x01, 0x6A, 0x00}
	var page byte
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.Page != page {
			out = append(out, tokenPageSwitch, n.Page)
			page = n.Page
		}
		switch {
		case len(n.Children) > 0:
			out = append(out, n.Token|contentFlag)
			for _, c := range n.Children {
				walk(c)
			}
			out = append(out, tokenEnd)
		case n.Opaque != nil:
			out = append(out, n.Token|contentFlag, tokenOpaque)
			// Opaque payloads are request-body bounded, far below uint32 max.
			out = appendMBUint32(out, uint32(len(n.Opaque))) //nolint:gosec // bodies bounded well below 4GiB
			out = append(out, n.Opaque...)
			out = append(out, tokenEnd)
		case n.Text != "":
			out = append(out, n.Token|contentFlag, tokenInlineStr)
			out = append(out, n.Text...)
			out = append(out, 0x00, tokenEnd)
		default:
			// Empty element: bare token without the content flag.
			out = append(out, n.Token)
		}
	}
	walk(root)
	return out
}

// appendMBUint32 appends a WBXML multi-byte unsigned integer.
func appendMBUint32(out []byte, v uint32) []byte {
	var buf [5]byte
	i := len(buf)
	buf[i-1] = byte(v & 0x7F)
	v >>= 7
	for v > 0 {
		i--
		buf[i-1] = byte(v&0x7F) | 0x80
		v >>= 7
	}
	return append(out, buf[i-1:]...)
}

// readMBUint32 reads a multi-byte unsigned integer starting at off.
func readMBUint32(in []byte, off int) (uint32, int, error) {
	var v uint32
	for i := 0; i < 5; i++ {
		if off >= len(in) {
			return 0, off, errors.New("eas/wbxml: truncated multi-byte integer")
		}
		b := in[off]
		off++
		v = v<<7 | uint32(b&0x7F)
		if b&0x80 == 0 {
			return v, off, nil
		}
	}
	return 0, off, errors.New("eas/wbxml: multi-byte integer too long")
}

// Decode parses a WBXML document produced by Encode (or any conforming
// MS-ASWBXML encoder that uses only inline strings and opaque content).
func Decode(in []byte) (*Node, error) {
	if len(in) < 4 {
		return nil, errors.New("eas/wbxml: short header")
	}
	if in[0] != wbxmlVersion13 {
		return nil, fmt.Errorf("eas/wbxml: unsupported WBXML version 0x%02x", in[0])
	}
	if in[2] != 0x6A {
		return nil, fmt.Errorf("eas/wbxml: unsupported charset 0x%02x", in[2])
	}
	off := 3
	strTableLen, off, err := readMBUint32(in, off)
	if err != nil {
		return nil, err
	}
	if int(strTableLen) > 0 {
		return nil, errors.New("eas/wbxml: string table not supported")
	}
	page := byte(0)
	node, off, err := decodeElement(in, off, &page, true, 0)
	if err != nil {
		return nil, err
	}
	if off != len(in) {
		return nil, fmt.Errorf("eas/wbxml: %d trailing bytes", len(in)-off)
	}
	return node, nil
}

// decodeElement reads one element token and its content.
func decodeElement(in []byte, off int, page *byte, top bool, depth int) (*Node, int, error) {
	if depth > 64 {
		return nil, off, errors.New("eas/wbxml: nesting limit")
	}
	for {
		if off >= len(in) {
			return nil, off, errors.New("eas/wbxml: truncated token")
		}
		tok := in[off]
		off++
		switch tok {
		case tokenPageSwitch:
			if off >= len(in) {
				return nil, off, errors.New("eas/wbxml: truncated page switch")
			}
			*page = in[off]
			off++
			if _, known := codePages[*page]; !known {
				return nil, off, fmt.Errorf("eas/wbxml: unknown code page %d", *page)
			}
			continue
		case tokenEnd:
			if top {
				return nil, off, errors.New("eas/wbxml: unexpected END at top level")
			}
			return nil, off, errEndSentinel
		case tokenInlineStr:
			return nil, off, errors.New("eas/wbxml: inline string outside element content")
		case tokenOpaque:
			return nil, off, errors.New("eas/wbxml: opaque outside element content")
		default:
			if tok&0x80 != 0 {
				return nil, off, errors.New("eas/wbxml: attributes unsupported")
			}
			hasContent := tok&contentFlag != 0
			tok &^= contentFlag
			if _, known := codePages[*page]; !known || tokens[*page][tok] == "" {
				return nil, off, fmt.Errorf("eas/wbxml: unknown token %s", TokenName(*page, tok))
			}
			n := &Node{Page: *page, Token: tok}
			if !hasContent {
				// Empty element: no content follows.
				return n, off, nil
			}
			var err error
			n, off, err = decodeContent(in, off, page, tok, n, depth)
			return n, off, err
		}
	}
}

// errEndSentinel marks a bare END token consumed by a parent frame.
var errEndSentinel = errors.New("eas/wbxml: end")

// decodeContent parses the content body following a token already consumed.
func decodeContent(in []byte, off int, page *byte, tok byte, n *Node, depth int) (*Node, int, error) {
	if off >= len(in) {
		return nil, off, fmt.Errorf("eas/wbxml: truncated content for %s", TokenName(*page, tok))
	}
	switch in[off] {
	case tokenInlineStr:
		off++
		start := off
		for off < len(in) && in[off] != 0x00 {
			off++
		}
		if off >= len(in) {
			return nil, off, errors.New("eas/wbxml: unterminated inline string")
		}
		n.Text = string(in[start:off])
		off++
		if off >= len(in) || in[off] != tokenEnd {
			return nil, off, errors.New("eas/wbxml: text missing END")
		}
		return n, off + 1, nil
	case tokenOpaque:
		off++
		size, off2, err := readMBUint32(in, off)
		if err != nil {
			return nil, off2, err
		}
		if int64(size) > int64(len(in)-off2) {
			return nil, off2, errors.New("eas/wbxml: truncated opaque content")
		}
		n.Opaque = append([]byte(nil), in[off2:off2+int(size)]...)
		end := off2 + int(size)
		if end >= len(in) || in[end] != tokenEnd {
			return nil, end, errors.New("eas/wbxml: opaque missing END")
		}
		return n, end + 1, nil
	case tokenEnd:
		return n, off + 1, nil
	default:
		// Element content: children until END.
		for {
			if off >= len(in) {
				return nil, off, errors.New("eas/wbxml: unterminated element content")
			}
			if in[off] == tokenEnd {
				return n, off + 1, nil
			}
			child, off2, err := decodeElement(in, off, page, false, depth+1)
			if errors.Is(err, errEndSentinel) {
				return n, off2, nil
			}
			if err != nil {
				return nil, off2, err
			}
			n.Children = append(n.Children, child)
			off = off2
		}
	}
}

// Child returns the first child with the given token. Pages switch freely
// inside a document, so the match is on token alone.
func (n *Node) Child(token byte) *Node {
	for _, c := range n.Children {
		if c.Token == token {
			return c
		}
	}
	return nil
}

// ChildText returns the inline text of the first matching child.
func (n *Node) ChildText(token byte) string {
	if c := n.Child(token); c != nil {
		return c.Text
	}
	return ""
}

// BinaryContent returns opaque data for binary-valued elements.
func (n *Node) BinaryContent() []byte { return n.Opaque }

var _ = binary.BigEndian // reserved for future integer token helpers
