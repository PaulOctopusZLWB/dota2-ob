//go:build linux

package session

// VerifyRawV3ContentGuard exposes the existing Linux raw inode guard as a
// read-only attestation for evidence consumers. It does not mutate capture.
func VerifyRawV3ContentGuard(path string) bool {
	valid, available := rawContentGuardState(path)
	return available && valid
}
