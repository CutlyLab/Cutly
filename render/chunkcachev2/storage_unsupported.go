//go:build !darwin && !linux

package chunkcachev2

import (
	"context"
	cachekey "github.com/CutlyLab/Cutly/cache/key"
)

type session struct{}

func (x *session) setHeartbeat(func() error) {}
func (x *session) check() error              { return ErrUnsupported }

func (x *session) admitPublication(cachekey.Key) error { return ErrUnsupported }

func (s *Store) initialize() error                                       { return ErrUnsupported }
func (s *Store) withSession(context.Context, func(*session) error) error { return ErrUnsupported }
func (x *session) read(cachekey.Key) ([]byte, error)                     { return nil, ErrUnsupported }
func (x *session) quarantine(cachekey.Key) error                         { return ErrUnsupported }
func (x *session) resync(cachekey.Key) error                             { return ErrUnsupported }
func (x *session) publish(context.Context, cachekey.Key, []byte) error   { return ErrUnsupported }
