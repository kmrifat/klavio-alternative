package contacts

// Status is where a contact stands with one brand. The same values are
// enforced by a CHECK constraint on contacts.status.
type Status string

const (
	StatusPendingConfirmation Status = "pending_confirmation" // double opt-in email sent, not clicked yet
	StatusSubscribed          Status = "subscribed"
	StatusQuarantined         Status = "quarantined" // imported with no activity for 12+ months: re-permission email only
	StatusUnsubscribed        Status = "unsubscribed"
)

// ConsentSource is how a consent record was collected.
type ConsentSource string

const (
	SourceAPI    ConsentSource = "api"
	SourceImport ConsentSource = "import"
	SourceManual ConsentSource = "manual"
)

// SuppressionReason says why an address may never be sent to again, by any brand.
type SuppressionReason string

const (
	SuppressHardBounce SuppressionReason = "hard_bounce"
	SuppressComplaint  SuppressionReason = "complaint"
	SuppressManual     SuppressionReason = "manual"
	SuppressErasure    SuppressionReason = "erasure"
)
