package delegation

import (
	"sync"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"
)

// correlation is CORE's side of one delegation, kept while its Harness Thread is
// in use so a confirmed Harness Event can be logged next to CORE's own IDs.
type correlation struct {
	traceID   string
	taskID    string
	turnID    string
	actionID  string
	attemptID string
}

// maxCorrelations bounds how many finished delegations stay correlated. The
// last Events of a Run (run.terminal) can reach the pump after the delegation
// has returned, so a Thread stays known for a while instead of being forgotten
// at once.
const maxCorrelations = 256

// correlations maps a Harness Thread to the delegation that uses it.
type correlations struct {
	mu    sync.Mutex
	byID  map[string]correlation
	order []string
}

func (c *correlations) put(thread string, value correlation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID == nil {
		c.byID = make(map[string]correlation, maxCorrelations)
	}
	if _, known := c.byID[thread]; !known {
		c.order = append(c.order, thread)
		if len(c.order) > maxCorrelations {
			delete(c.byID, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.byID[thread] = value
}

func (c *correlations) get(thread string) (correlation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.byID[thread]
	return value, ok
}

func (c correlation) fields() map[string]any {
	return map[string]any{
		"trace_id": c.traceID, "task_id": c.taskID, "turn_id": c.turnID,
		"action_id": c.actionID, "attempt_id": c.attemptID,
	}
}

// pump drains the notifications of one client until it is closed. It never
// blocks on anything but the channel: a consumer that falls behind makes the
// client end the connection (events are durable in the Harness store and are
// read again with events/read), so this loop only records and returns.
//
// The confirmed Events are the Harness's own facts. CORE does not copy them
// into its Canonical Event Store; it records a small reference line (the owner
// and the Event ID, the type, the sequence, the IDs) for the Events that tell how
// a delegation went, and nothing of their payload, which may hold the Model's or
// a Tool's content.
func (r *Runtime) pump(c Client) {
	for n := range c.Notifications() {
		if n.Kind != client.NotificationEvent || n.Event == nil {
			continue // provisional progress is not a durable fact
		}
		r.observe(*n.Event)
	}
}

// loggedEventTypes are the confirmed Event types CORE records.
var loggedEventTypes = map[string]bool{
	protocol.EventTaskCreated:          true,
	protocol.EventRunStarted:           true,
	protocol.EventControlCancelRequest: true,
	protocol.EventCheckpointCommitted:  true,
	protocol.EventRunTerminal:          true,
}

func (r *Runtime) observe(event protocol.Event) {
	if !loggedEventTypes[event.Type] && event.Code == nil {
		return
	}
	fields := map[string]any{
		"harness_event":  map[string]any{"owner": "RenCrow_Harness", "event_id": event.EventID},
		"harness_thread": harnessRef(event.ThreadID),
		"event_type":     event.Type,
		"event_seq":      event.EventSeq,
	}
	if event.TaskID != nil {
		fields["harness_task"] = harnessRef(*event.TaskID)
	}
	if event.RunID != nil {
		fields["harness_run"] = harnessRef(*event.RunID)
	}
	if event.ReceiptID != nil {
		fields["harness_receipt"] = harnessRef(*event.ReceiptID)
	}
	if event.Code != nil {
		fields["code"] = *event.Code
	}
	if known, ok := r.threads.get(event.ThreadID); ok {
		for key, value := range known.fields() {
			fields[key] = value
		}
	}
	r.log.emit("info", eventHarnessEvent, fields)
}
