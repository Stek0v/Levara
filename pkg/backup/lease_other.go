//go:build !darwin && !linux

package backup

import "errors"

var ErrSourceActive = errors.New("backup: data root has an active writer or backup")

type Lease struct{}

func (*Lease) Close() error { return nil }
func AcquireWriterLease(string) (*Lease, error) {
	return nil, errors.New("backup: native writer lock unsupported on this platform")
}
func AcquireOfflineLease(string) (*Lease, error) {
	return nil, errors.New("backup: native writer lock unsupported on this platform")
}
