package superagent

import (
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const runQueueActorCandidateLimit = 5001

func runQueueItemExactActorClaimable(item domainsuperagent.RunQueueItem, now time.Time) bool {
	due := item.NotBefore.IsZero() || !item.NotBefore.After(now)
	if !due || item.RunID != "" {
		return false
	}
	if item.Status == "queued" {
		return item.AttemptCount == 0
	}
	// A reserved item with no attached canonical Run is safe to reacquire only
	// after its lease expires. If StartRun succeeded before a crash, the
	// canonical Task is no longer queued and the scheduler will reject it.
	return item.Status == "reserved" && item.AttemptCount > 0 &&
		!item.LeaseUntil.IsZero() && !item.LeaseUntil.After(now)
}

func reserveExactRunQueueItem(item *domainsuperagent.RunQueueItem, now, leaseUntil time.Time, leaseToken string) {
	if item.Status == "reserved" {
		// Reacquire a pre-Run reservation as its first execution attempt. Do not
		// turn this into RunStartReasonLeaseReacquire; no Run was attached.
		item.AttemptCount = 0
	}
	item.Status = "reserved"
	item.ClaimedAt = now
	item.LeaseToken = leaseToken
	item.LeaseUntil = leaseUntil
	item.RunID = ""
	item.AttemptCount++
	item.CompletedAt = time.Time{}
}

func releaseExactRunQueueItem(item *domainsuperagent.RunQueueItem) {
	item.Status = "queued"
	item.ClaimedAt = time.Time{}
	item.LeaseToken = ""
	item.LeaseUntil = time.Time{}
	if item.AttemptCount > 0 {
		item.AttemptCount--
	}
	item.CompletedAt = time.Time{}
	item.RunID = ""
}

func validExactRunQueueLease(queueID modulecore.QueueItemID, token string) bool {
	return queueID.Validate() == nil && token != ""
}
