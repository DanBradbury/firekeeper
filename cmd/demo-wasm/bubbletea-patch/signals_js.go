//go:build js

package tea

func (p *Program) listenForResize(done chan struct{}) { <-done }
