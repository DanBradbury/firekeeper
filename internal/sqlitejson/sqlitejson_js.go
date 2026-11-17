package sqlitejson

import (
	"context"
	"errors"
)

var ErrArguments = errors.New("sqlitejson: expected -readonly -json <database> <query>")

func Run(context.Context, ...string) ([]byte, error) {
	return nil, errors.New("sqlitejson: unavailable in browser demo")
}

func RunInProcess(context.Context, ...string) ([]byte, error) {
	return nil, errors.New("sqlitejson: unavailable in browser demo")
}
