package toll

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs the package under goleak so any Limiter that forgets to Close
// (leaking its rotator goroutine) fails the suite.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
