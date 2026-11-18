package bind

import "sync"

// Installer builds and binds one package (it calls back into the
// engine's Bind, which is what lands the package in Names).
type Installer func()

// Pending is the lazy half of the bound-package registry: the per-path
// installers a session registered but has not run yet. Install runs a
// path's installer on first use — the entry P2-12's lazy stdlib goes
// through — and concurrent first uses wait on each other, so a package
// is built exactly once per registry.
//
// Pending owns its own lock: Install runs the installer WITHOUT any
// lock held, so the installer's Bind can take the engine lock; the
// read methods (Registered, Paths) are safe to call under that lock.
type Pending struct {
	mu         sync.Mutex
	installers map[string]Installer
	installing map[string]chan struct{}
}

func NewPending() *Pending {
	return &Pending{
		installers: map[string]Installer{},
		installing: map[string]chan struct{}{},
	}
}

// Register records path's lazy installer.
func (p *Pending) Register(path string, f Installer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.installers[path] = f
}

// Clear drops a pending installer — an explicit Bind of the same path
// wins over the lazy default.
func (p *Pending) Clear(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.installers, path)
}

// Registered reports whether path has a pending or in-flight install.
func (p *Pending) Registered(path string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.registered(path)
}

func (p *Pending) registered(path string) bool {
	if _, ok := p.installers[path]; ok {
		return true
	}
	_, ok := p.installing[path]
	return ok
}

// Paths returns the paths with a pending or in-flight install.
func (p *Pending) Paths() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	paths := make([]string, 0, len(p.installers)+len(p.installing))
	for path := range p.installers {
		paths = append(paths, path)
	}
	for path := range p.installing {
		paths = append(paths, path)
	}
	return paths
}

// Install runs path's pending installer if any, waiting out an install
// already in flight on another goroutine. Unregistered paths are a
// no-op.
func (p *Pending) Install(path string) {
	p.mu.Lock()
	f, ok := p.installers[path]
	if ok {
		delete(p.installers, path)
		done := make(chan struct{})
		p.installing[path] = done
		p.mu.Unlock()

		defer func() {
			p.mu.Lock()
			delete(p.installing, path)
			p.mu.Unlock()
			close(done)
		}()
		f()
		return
	}
	done := p.installing[path]
	p.mu.Unlock()
	if done != nil {
		<-done
	}
}
