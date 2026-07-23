package wdtt

import "sync"

type processRegistry struct {
	binary     string
	runtimeDir string
	mu         sync.Mutex
	procs      map[string]*process
}

func newProcessRegistry(binary, runtimeDir string) *processRegistry {
	return &processRegistry{binary: binary, runtimeDir: runtimeDir}
}

func (r *processRegistry) get(id string) *process {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.procs == nil {
		r.procs = make(map[string]*process)
	}
	if p, ok := r.procs[id]; ok {
		return p
	}
	pidName := "client"
	if id != DefaultInstanceID {
		pidName = "client-" + id
	}
	p := newProcess(pidName, r.binary, r.runtimeDir)
	r.procs[id] = p
	return p
}

func (r *processRegistry) stopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.procs {
		_ = p.Stop()
	}
}
