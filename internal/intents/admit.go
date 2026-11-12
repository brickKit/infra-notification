package intents

import core "github.com/brickKit/infra-notification/v3/internal/core"

// Arrival is what Admit judges of one arriving intent (be-protocol P18.7-P18.9; the vector notification-intent's
// admit step).
type Arrival struct {
	Kind                 string
	SenderIsSource       bool // the payload's sender and the envelope's source are the owner of the stream it came on
	ShapeOK              bool // the payload follows be.notification.intent.v1
	Category             string
	ExpiresAfterOccurred bool
	Seen                 bool   // this sender's intent ID was taken in before
	CollapseSeq          *int64 // nil: no merge key
	MaxSeq               *int64 // the merge key's largest sequence seen; nil: none
}

// Admission is Admit's answer: Accepted, Duplicate, Stale or a refusal's reason; whether the intent supersedes the
// earlier ones of its merge key, and whether it is delivered.
type Admission struct {
	Answer     string
	Supersedes bool
	Delivers   bool
}

// The answers that are not refusals.
const (
	Accepted  = "accepted"
	Duplicate = "duplicate"
	Stale     = "stale"
)

// Admit decides an arriving intent (NT02, NT04): the refusals in P18.8's order (the shape, the sender, the category,
// the expiry), then the duplicate (P18.9), then the
// merge key's sequence (P18.7).
func Admit(a Arrival) Admission {
	_, known := core.CategoryOf(a.Category)
	switch {
	case !a.ShapeOK: // first: a payload off the contract has no sender to trust
		return Admission{Answer: core.Malformed}
	case !a.SenderIsSource:
		return Admission{Answer: core.NotTheSender}
	case !known:
		return Admission{Answer: core.UnknownCategory}
	case a.Kind == "deliver" && !a.ExpiresAfterOccurred:
		return Admission{Answer: core.Malformed}
	case a.Seen:
		return Admission{Answer: Duplicate}
	case a.CollapseSeq != nil && a.MaxSeq != nil && *a.CollapseSeq <= *a.MaxSeq:
		return Admission{Answer: Stale}
	}
	return Admission{Answer: Accepted, Supersedes: a.CollapseSeq != nil, Delivers: a.Kind == "deliver"}
}
