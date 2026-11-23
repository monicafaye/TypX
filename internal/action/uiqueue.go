package action

import "sync"

var (
	uiQueueMu    sync.Mutex
	uiQueue      []func()
	uiQueueReady = make(chan struct{}, 1)
)

// EnqueueUI schedules f to run on the main (UI) goroutine.
//
// Frozen contract (Phase 2/3):
//   - Safe to call from ANY goroutine, including the main goroutine itself.
//   - Never blocks and never drops: backed by an unbounded mutex-protected queue
//     and a 1-slot wake channel.
//   - FIFO execution order. Callbacks run between input events; the main loop
//     redraws after every drained batch, so callers never need screen.Redraw().
//   - Callbacks enqueued while a batch is draining are deferred to the next batch
//     so user input is never starved.
//   - nil is ignored. A panic inside f follows the normal main-loop panic path
//     (unsaved buffers are backed up and the editor exits).
//   - Background goroutines MUST use EnqueueUI before touching buffer.Buffer,
//     display, action.Tabs, InfoBar, or tcell.Screen.
func EnqueueUI(f func()) {
	if f == nil {
		return
	}

	uiQueueMu.Lock()
	uiQueue = append(uiQueue, f)
	uiQueueMu.Unlock()

	select {
	case uiQueueReady <- struct{}{}:
	default:
	}
}

// UIQueueReady returns a channel that is signalled whenever one or more
// callbacks have been queued via EnqueueUI. Intended for the main event loop.
func UIQueueReady() <-chan struct{} {
	return uiQueueReady
}

// DrainUIQueue executes all currently queued callbacks in FIFO order on the
// calling (main) goroutine and returns the number of callbacks executed.
// Callbacks enqueued while DrainUIQueue is executing are deferred to the next
// DrainUIQueue call.
func DrainUIQueue() int {
	uiQueueMu.Lock()
	if len(uiQueue) == 0 {
		uiQueueMu.Unlock()
		return 0
	}
	batch := uiQueue
	uiQueue = nil
	uiQueueMu.Unlock()

	for _, fn := range batch {
		fn()
	}

	uiQueueMu.Lock()
	more := len(uiQueue) > 0
	uiQueueMu.Unlock()
	if more {
		select {
		case uiQueueReady <- struct{}{}:
		default:
		}
	}

	return len(batch)
}
