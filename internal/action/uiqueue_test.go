package action

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetUIQueueForTest() {
	DrainUIQueue()
	select {
	case <-UIQueueReady():
	default:
	}
}

func TestUIQueueFIFOAndNil(t *testing.T) {
	resetUIQueueForTest()
	defer resetUIQueueForTest()

	EnqueueUI(nil)
	select {
	case <-UIQueueReady():
		t.Fatal("nil callback must not signal UIQueueReady")
	default:
	}
	assert.Equal(t, 0, DrainUIQueue())

	var got []int
	for i := 1; i <= 5; i++ {
		n := i
		EnqueueUI(func() { got = append(got, n) })
	}

	select {
	case <-UIQueueReady():
	default:
		t.Fatal("expected UIQueueReady to be signalled")
	}

	assert.Equal(t, 5, DrainUIQueue())
	assert.Equal(t, []int{1, 2, 3, 4, 5}, got)
	assert.Equal(t, 0, DrainUIQueue())
}

func TestUIQueueReentrantDeferredToNextBatch(t *testing.T) {
	resetUIQueueForTest()
	defer resetUIQueueForTest()

	var order []string
	EnqueueUI(func() {
		order = append(order, "first")
		EnqueueUI(func() {
			order = append(order, "nested")
		})
	})
	EnqueueUI(func() {
		order = append(order, "second")
	})

	<-UIQueueReady()
	assert.Equal(t, 2, DrainUIQueue())
	assert.Equal(t, []string{"first", "second"}, order)

	// Nested callback must have re-signalled UIQueueReady for the next iteration.
	select {
	case <-UIQueueReady():
	default:
		t.Fatal("expected UIQueueReady to be re-signalled for nested callback")
	}

	assert.Equal(t, 1, DrainUIQueue())
	assert.Equal(t, []string{"first", "second", "nested"}, order)
}

func TestUIQueueConcurrentProducers(t *testing.T) {
	resetUIQueueForTest()
	defer resetUIQueueForTest()

	const (
		producers = 64
		perWorker = 500
		total     = producers * perWorker
	)

	seen := make([]int, producers)
	for i := range seen {
		seen[i] = -1
	}

	var wg sync.WaitGroup
	wg.Add(producers)
	for p := 0; p < producers; p++ {
		pid := p
		go func() {
			defer wg.Done()
			for seq := 0; seq < perWorker; seq++ {
				s := seq
				EnqueueUI(func() {
					require.Equal(t, seen[pid]+1, s, "out-of-order delivery for producer %d", pid)
					seen[pid] = s
				})
			}
		}()
	}

	drained := 0
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	for drained < total {
		select {
		case <-UIQueueReady():
			drained += DrainUIQueue()
		case <-done:
			drained += DrainUIQueue()
		}
	}

	assert.Equal(t, total, drained)
	for p := 0; p < producers; p++ {
		assert.Equal(t, perWorker-1, seen[p])
	}
}
