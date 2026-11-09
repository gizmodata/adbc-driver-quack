package quack

import "github.com/gizmodata/adbc-driver-quack/internal/transport"

// Version is the driver release version, reported as ADBC's
// InfoDriverVersion, in the Quack handshake and in the HTTP User-Agent.
// Keep it in sync with python/adbc_driver_quack/_version.py.
const Version = "1.0.1"

func init() {
	transport.UserAgent = "adbc-driver-quack/" + Version
}
