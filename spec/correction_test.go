package spec

import "testing"

func TestNearestCommandNamesCorrectsTransposition(t *testing.T) {
	got := nearestCommandNames("gti", []string{"grep", "go", "git"}, 2)
	if len(got) == 0 || got[0] != "git" {
		t.Fatalf("expected git as nearest command, got %v", got)
	}
}
