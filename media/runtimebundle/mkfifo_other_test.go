//go:build !unix

package runtimebundle

import "errors"

func makeNamedPipe(string) error { return errors.New("named pipes unavailable") }
