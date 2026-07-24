package main

import "sync"

type paneLock struct {
	mu   sync.Mutex
	refs int
}

type paneLockSet struct {
	mu    sync.Mutex
	locks map[string]*paneLock
}

var paneOperations = paneLockSet{locks: make(map[string]*paneLock)}

func (s *paneLockSet) run(paneID string, operation func() (string, error)) (string, error) {
	s.mu.Lock()
	lock := s.locks[paneID]
	if lock == nil {
		lock = &paneLock{}
		s.locks[paneID] = lock
	}
	lock.refs++
	s.mu.Unlock()

	lock.mu.Lock()
	defer func() {
		lock.mu.Unlock()
		s.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.locks, paneID)
		}
		s.mu.Unlock()
	}()

	return operation()
}
