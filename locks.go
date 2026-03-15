package main

import "sync"

type WorkspaceLockManager struct {
	locks map[string]string
	mu    sync.Mutex
}

func NewWorkspaceLockManager() *WorkspaceLockManager {
	return &WorkspaceLockManager{locks: map[string]string{}}
}

func (wl *WorkspaceLockManager) TryLock(files []string, taskID string) (bool, string) {
	wl.mu.Lock()
	defer wl.mu.Unlock()
	for _, file := range files {
		if holder, ok := wl.locks[file]; ok && holder != taskID {
			return false, holder
		}
	}
	for _, file := range files {
		wl.locks[file] = taskID
	}
	return true, ""
}

func (wl *WorkspaceLockManager) Unlock(taskID string) {
	wl.mu.Lock()
	defer wl.mu.Unlock()
	for file, holder := range wl.locks {
		if holder == taskID {
			delete(wl.locks, file)
		}
	}
}
