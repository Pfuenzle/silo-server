//go:build integration

package integration

import "testing"

func TestAvailablePorts_ReturnsDistinctListeners(t *testing.T) {
	// Given
	main, jelly, abs := availablePorts(t)

	// Then
	if main == jelly || main == abs || jelly == abs {
		t.Fatalf("listener ports must be distinct: main=%d jelly=%d abs=%d", main, jelly, abs)
	}
}
