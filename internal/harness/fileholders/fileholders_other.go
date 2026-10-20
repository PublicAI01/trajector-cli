//go:build !windows

package fileholders

import "errors"

func of(string) ([]Holder, error) { return nil, errors.ErrUnsupported }
