package eas

// SupportedVersions are the negotiated protocol versions this transport
// accepts, per EAS_EVIDENCE.md:
// only protocol versions actually handled by the command layer are advertised.
// Document revision numbers (MS-ASHTTP 23.0, MS-ASWBXML 24.0, MS-ASCMD 28.0)
// are NOT protocol versions and must never appear here.
var SupportedVersions = []string{"14.1"}

// Commands advertised in MS-ASProtocolCommands. This is exactly the subset
// EAS_EVIDENCE.md section 2 assigns to this phase.
const (
	CmdSendMail       = "SendMail"
	CmdItemOperations = "ItemOperations"
	CmdSmartReply     = "SmartReply"
	CmdSmartForward   = "SmartForward"
	CmdFolderSync     = "FolderSync"
	CmdSync           = "Sync"
	CmdPing           = "Ping"
	CmdProvision      = "Provision"
)

// AdvertisedCommands is the value of the MS-ASProtocolCommands header.
const AdvertisedCommands = "FolderSync,Sync,Ping,Provision,SendMail,SmartReply,SmartForward,ItemOperations"

// AdvertisedVersions is the value of the MS-ASProtocolVersions header.
const AdvertisedVersions = "14.1"

// versionSet indexes SupportedVersions for validation.
var versionSet = func() map[string]bool {
	m := make(map[string]bool, len(SupportedVersions))
	for _, v := range SupportedVersions {
		m[v] = true
	}
	return m
}()

// VersionSupported reports whether the negotiated version is implemented.
func VersionSupported(v string) bool { return versionSet[v] }

// Commands outside the implemented subset. They must be refused with a
// protocol-visible failure, never a pseudo-success (EAS_EVIDENCE.md).
var refusedCommands = map[string]bool{
	"GetItemEstimate": true, "GetAttachment": true,
	"MeetingResponse": true,
	"MoveItems":       true, "Search": true,
	"Settings": true, "ResolveRecipients": true, "ValidateCert": true,
	"Find": true, "Autodiscover": true,
}

// MS-ASWBXML code page tokens used by the command and data layers.
const (
	pageAirSync         = byte(0)
	pageContacts        = byte(1) // MS-ASCNTC contact class
	pageEmail           = byte(2) // MS-ASEMAIL email class
	pageCalendar        = byte(4) // MS-ASCAL calendar class
	pageFolderHierarchy = byte(7)
	pagePing            = byte(13)
	pageProvision       = byte(14)
	pageAirSyncBase     = byte(17) // MS-ASAIRS body elements
)

// Email class (page 2) tokens from the implemented MS-ASEMAIL subset.
const (
	tokEmailDateReceived = byte(0x0F)
	tokEmailSubject      = byte(0x14)
	tokEmailRead         = byte(0x15)
	tokEmailTo           = byte(0x16)
	tokEmailCc           = byte(0x17)
	tokEmailFrom         = byte(0x18)
	tokEmailFlag         = byte(0x3A)
	tokEmailFlagStatus   = byte(0x3B)
)

// Contact class (page 1) tokens from the implemented MS-ASCNTC subset.
const (
	tokContactFileAs              = byte(0x1E)
	tokContactFirstName           = byte(0x1F)
	tokContactLastName            = byte(0x29)
	tokContactEmail1Address       = byte(0x1B)
	tokContactEmail2Address       = byte(0x1C)
	tokContactEmail3Address       = byte(0x1D)
	tokContactBusinessPhoneNumber = byte(0x13)
	tokContactHomePhoneNumber     = byte(0x27)
	tokContactMobilePhoneNumber   = byte(0x2B)
)

// Calendar class (page 4) tokens from the implemented MS-ASCAL subset.
const (
	tokCalAllDayEvent  = byte(0x06)
	tokCalBusyStatus   = byte(0x0D)
	tokCalDtStamp      = byte(0x11)
	tokCalEndTime      = byte(0x12)
	tokCalExceptions   = byte(0x14)
	tokCalException    = byte(0x13)
	tokCalExcDeleted   = byte(0x15)
	tokCalExcStartTime = byte(0x16)
	tokCalLocation     = byte(0x17)
	tokCalRecurrence   = byte(0x1B)
	tokCalRecType      = byte(0x1C)
	tokCalRecUntil     = byte(0x1D)
	tokCalRecCount     = byte(0x1E)
	tokCalRecInterval  = byte(0x1F)
	tokCalSubject      = byte(0x26)
	tokCalStartTime    = byte(0x27)
	tokCalUID          = byte(0x28)
)

// AirSyncBase (page 17) tokens used for Body elements inside any class.
// Values per MS-ASWBXML 2.2.2.17 (the pre-EAS-003 table below is corrected
// here because page 17 was never exercised before data sync existed).
const (
	tokBaseType              = byte(0x06)
	tokBaseBody              = byte(0x0A)
	tokBaseData              = byte(0x0B)
	tokBaseEstimatedDataSize = byte(0x0C)
	tokBaseTruncated         = byte(0x0D)
	tokBasePreview           = byte(0x18)
)

// AirSync (page 0) response tokens for per-item client command results.
const (
	tokResponses = byte(0x06)
	tokAdd       = byte(0x07)
	tokChange    = byte(0x08)
	tokDelete    = byte(0x09)
	tokFetch     = byte(0x0A)
	tokClientID  = byte(0x0C)
)

// AirSync (page 0) tokens.
const (
	tokSync            = byte(0x05)
	tokSyncKey         = byte(0x0B)
	tokServerID        = byte(0x0D)
	tokStatus          = byte(0x0E)
	tokCollection      = byte(0x0F)
	tokClass           = byte(0x10)
	tokCollectionID    = byte(0x12)
	tokGetChanges      = byte(0x13)
	tokMoreAvailable   = byte(0x14)
	tokWindowSize      = byte(0x15)
	tokCollections     = byte(0x1C)
	tokApplicationData = byte(0x1D)
)

// FolderHierarchy (page 7) tokens.
const (
	tokFolders     = byte(0x05)
	tokFolderSync  = byte(0x16)
	tokFCount      = byte(0x17)
	tokFolder      = byte(0x06)
	tokFVersion    = byte(0x0B)
	tokFSyncKey    = byte(0x12)
	tokFolderID    = byte(0x08)
	tokParentID    = byte(0x09)
	tokDisplayName = byte(0x07)
	tokType        = byte(0x0A)
	tokFStatus     = byte(0x0C)
	tokChanges     = byte(0x0E)
	tokFAdd        = byte(0x0F)
	tokFDelete     = byte(0x10)
	tokFUpdate     = byte(0x11)
)

// Ping (page 13) tokens.
const (
	tokPing        = byte(0x05)
	tokPStatus     = byte(0x07)
	tokHeartbeat   = byte(0x08)
	tokPFolders    = byte(0x09)
	tokPFolder     = byte(0x0A)
	tokPId         = byte(0x0B)
	tokPClass      = byte(0x0C)
	tokPFolderType = byte(0x0D)
)

// Provision (page 14) tokens.
const (
	tokProvision     = byte(0x05)
	tokPolicies      = byte(0x06)
	tokPolicy        = byte(0x07)
	tokPolicyType    = byte(0x08)
	tokPolicyKey     = byte(0x09)
	tokPData         = byte(0x0A)
	tokProvStatus    = byte(0x0B)
	tokRemoteWipe    = byte(0x0C)
	tokEASDoc        = byte(0x0D)
	tokDevPwdEnabled = byte(0x0E)
)

// FolderSync status values (MS-ASCMD FolderSync Status element).
const (
	folderSyncSuccess       = "1"
	folderSyncProtocolError = "10"
	folderSyncServerError   = "6"
	folderSyncInvalidKey    = "9"
)

// Sync command status values (MS-ASCMD Sync Status). Top-level Status only
// appears for protocol-level errors; per-collection Status carries the rest.
const (
	syncStatusSuccess       = "1" // collection synced
	syncStatusProtocolError = "4" // top-level: malformed request
	syncStatusServerError   = "5" // collection: backend unavailable
	syncStatusObjectDeleted = "8" // unknown or foreign collection id
	syncStatusInvalidKey    = "3" // sync key no longer valid: full resync
)

// Ping status values (MS-ASCMD Ping Status element). A Ping that expires
// without changes returns 1; this is NOT a sync success of any kind.
const (
	pingNoChanges     = "1"
	pingChanges       = "2"
	pingProtocolError = "4"
	pingServerError   = "8"
	pingHbOutOfRange  = "5"
)

// Provision policy status values (MS-ASPROV Policy Status element).
const (
	provSuccess               = "1"
	provProtocolError         = "2"
	provServerError           = "3"
	provPolicyTypeUnsupported = "4"
)

// EAS folder type values (MS-ASCMD FolderSync Type element).
const (
	folderTypeUserCreatedEmail = 1
	folderTypeInbox            = 2
	folderTypeDrafts           = 3
	folderTypeDeleted          = 4
	folderTypeSent             = 5
	folderTypeOutbox           = 6
	folderTypeCalendarDefault  = 8
	folderTypeContactsDefault  = 9
)
