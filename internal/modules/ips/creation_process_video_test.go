package ips

import "testing"

func TestCreationProcessVideoSizeLimitIs300MB(t *testing.T) {
	const want int64 = 300 * 1024 * 1024
	if creationProcessVideoMaxBytes != want {
		t.Fatalf("creationProcessVideoMaxBytes=%d, want %d", creationProcessVideoMaxBytes, want)
	}
}
