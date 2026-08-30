package main

import "testing"

func TestIsSTTFinalControl(t *testing.T) {
	for _, control := range []string{"final_pending", " stop "} {
		if !isSTTFinalControl(control) {
			t.Fatalf("expected %q to request finalization", control)
		}
	}
	for _, control := range []string{"start", "config", ""} {
		if isSTTFinalControl(control) {
			t.Fatalf("did not expect %q to request finalization", control)
		}
	}
}
