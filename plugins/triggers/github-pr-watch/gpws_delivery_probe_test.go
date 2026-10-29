package main

import "testing"

// TestGPWSDeliveryProbe is an intentional failure on the isolated delivery fixture only.
func TestGPWSDeliveryProbe(t *testing.T) {
	t.Fatal("GPWS-D01 intentional delivery stimulus; this fixture must not be merged")
}
