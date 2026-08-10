package mirror

// BuildManifest derives the per set manifest from state alone. Clients diff
// Hash to decide which sets to re-download, and use Count and Bytes to size
// that download before starting it.
//
// This reads nothing from the bucket. Every input is already in state, so the
// manifest costs no I/O and cannot drift from what was actually stored.
func BuildManifest(state State, want map[string]Image) Manifest {
	digests := SetDigests(state, want)
	out := make(Manifest, len(digests))
	for code, byKey := range digests {
		var bytes int64
		for key := range byKey {
			bytes += state[key].Size
		}
		out[code] = ImageInfo{Hash: SetHash(byKey), Count: len(byKey), Bytes: bytes}
	}
	return out
}
