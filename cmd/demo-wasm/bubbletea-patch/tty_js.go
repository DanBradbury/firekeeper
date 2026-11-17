//go:build js

package tea

import "os"

func (p *Program) initInput() error   { return nil }
func openInputTTY() (*os.File, error) { return nil, os.ErrNotExist }

const suspendSupported = false

func suspendProcess() {}
