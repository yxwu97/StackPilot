// Package schemas exposes immutable machine-readable StackPilot schemas.
package schemas

import _ "embed"

//go:embed system-v1alpha1.schema.json
var systemV1Alpha1 []byte

// SystemV1Alpha1 returns an isolated copy of the manifest Schema bytes.
func SystemV1Alpha1() []byte {
	return append([]byte(nil), systemV1Alpha1...)
}
