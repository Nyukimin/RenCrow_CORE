package main

import (
	"context"
	"log"
	"time"

	browsertraceapp "github.com/Nyukimin/RenCrow_CORE/internal/application/browsertrace"
)

// recoverBrowserTraceAPICreationFacts retries every browsertrace APIArtifact creation fact
// that the artifact store still holds as undelivered. It runs on this start, after the
// artifact store and the canonical event store are both open and before the route is
// advertised, because a request that stored an artifact and then lost the process leaves
// the fact durable only in the artifact store: a caller resending the request would mint
// new artifacts, so only an explicit pass on start can finish the earlier delivery.
//
// It reports a failed pass instead of stopping the process. The intents are the evidence
// and they stay in place, so a later start retries the same facts; nothing here erases or
// rewrites a fact to make this start look clean.
//
// The pass is time-bounded so a canonical event store that cannot answer does not hold
// process start open forever. A pass that runs out of time leaves the facts it has not
// delivered in the artifact store, which is exactly the state the next start retries.
const browserTraceCreationFactRecoveryTimeout = 30 * time.Second

func recoverBrowserTraceAPICreationFacts(ctx context.Context, source browsertraceapp.APIArtifactPublicationSource, events browsertraceapp.APIArtifactCreationPublisher) error {
	passCtx, cancel := context.WithTimeout(ctx, browserTraceCreationFactRecoveryTimeout)
	defer cancel()
	report, err := browsertraceapp.PublishPendingAPIArtifactCreationFacts(passCtx, source, events)
	if report.Scanned > 0 {
		log.Printf("Browser Trace API creation fact recovery: published=%d confirmed=%d failed=%d of %d intents",
			report.Published, report.Confirmed, report.Failed, report.Scanned)
	}
	return err
}

// browserTraceSupersessionFactRecoveryTimeout bounds the supersession pass on its own, so
// one canonical event store that cannot answer does not hold process start open past the
// bound the creation pass already used.
const browserTraceSupersessionFactRecoveryTimeout = 30 * time.Second

// recoverBrowserTraceAPISupersessionFacts retries every browsertrace APIArtifact
// supersession fact that the artifact store still holds as undelivered. An edge and its
// fact are durable together in the artifact store, so a process that established an edge
// and then lost the canonical append leaves an event that only this pass can finish:
// reading the edge back cannot recover an EventID, and a caller resending the supersede
// would either be refused as already superseded or mint a new successor.
//
// It reports a failed pass instead of stopping the process, and it erases nothing: the
// facts stay in the artifact store, so a later start retries the same delivery.
func recoverBrowserTraceAPISupersessionFacts(ctx context.Context, source browsertraceapp.APIArtifactSupersessionSource, events browsertraceapp.APIArtifactCreationPublisher) error {
	passCtx, cancel := context.WithTimeout(ctx, browserTraceSupersessionFactRecoveryTimeout)
	defer cancel()
	report, err := browsertraceapp.PublishPendingAPIArtifactSupersessionFacts(passCtx, source, events)
	if report.Scanned > 0 {
		log.Printf("Browser Trace API supersession fact recovery: published=%d confirmed=%d failed=%d of %d facts",
			report.Published, report.Confirmed, report.Failed, report.Scanned)
	}
	return err
}
