package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPaneLockSerializesSamePane(t *testing.T) {
	locks := paneLockSet{locks: make(map[string]*paneLock)}
	var active int32
	var overlap int32
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = locks.run("w1:p1", func() (string, error) {
				if atomic.AddInt32(&active, 1) != 1 {
					atomic.StoreInt32(&overlap, 1)
				}
				time.Sleep(10 * time.Millisecond)
				atomic.AddInt32(&active, -1)
				return "", nil
			})
		}()
	}
	wg.Wait()
	if atomic.LoadInt32(&overlap) != 0 {
		t.Fatal("operations for one pane overlapped")
	}
}

func TestPaneLockAllowsDifferentPanes(t *testing.T) {
	locks := paneLockSet{locks: make(map[string]*paneLock)}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup

	for _, paneID := range []string{"w1:p1", "w1:p2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, _ = locks.run(id, func() (string, error) {
				started <- struct{}{}
				<-release
				return "", nil
			})
		}(paneID)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first pane did not start")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("different pane was unnecessarily blocked")
	}
	close(release)
	wg.Wait()
}
