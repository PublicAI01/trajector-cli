package lifecycle

import (
	"math"

	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// handshake is what the service last said alongside an acknowledged
// upload. The zero value stands in for a machine that never uploaded.
func (m *Machine) handshake() platform.Handshake {
	return upload.LoadHandshake(m.deps.Layout.UploadDir())
}

// spool opens the capture spool with the quota the service last set
// through the upload handshake; a machine that never uploaded runs on
// the default. Every surface answers "what is the quota" this one way.
func (m *Machine) spool() (*spool.Spool, error) {
	return spool.Create(m.deps.Layout.SpoolDir(), m.handshake().SpoolQuotaBytes)
}

// spoolUnbounded opens the spool with no quota at all. It exists for
// requeue alone: the quota bounds what recording may accumulate, not
// what already-captured data may return, and refusing the move would
// strand records that can only leave through the spool. The user asked
// for the requeue by name.
func (m *Machine) spoolUnbounded() (*spool.Spool, error) {
	return spool.Create(m.deps.Layout.SpoolDir(), math.MaxInt64)
}
