//go:build !linux && !darwin && !windows

package sandbox

import "errors"

func platformNativeRunner() Runner {
	return errRunner{name: "unsupported", Err: errors.New("native sandbox is not implemented for this GOOS")}
}
