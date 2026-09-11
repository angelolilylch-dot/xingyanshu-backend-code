package database

import "testing"

func TestChecksumIsStableAndContentSensitive(t *testing.T) {
	first := checksum([]byte("CREATE TABLE example (id BIGINT);"))
	second := checksum([]byte("CREATE TABLE example (id BIGINT);"))
	changed := checksum([]byte("CREATE TABLE example (id INT);"))

	if first != second {
		t.Fatalf("expected checksum to be stable")
	}
	if first == changed {
		t.Fatalf("expected checksum to change when migration content changes")
	}
}
