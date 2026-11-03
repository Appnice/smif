package claude

import "testing"

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
		same bool
	}{
		{"2.1.317", "2.1.316", 1, true}, {"2.1.316", "2.1.316", 0, true}, {"2.1.30", "2.1.316", -1, true},
		{"2.2.0", "2.1.316", 1, false}, {"3.0", "2.1.316", 1, false},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := SameMinor(c.a, c.b); got != c.same {
			t.Errorf("SameMinor(%s, %s) = %v", c.a, c.b, got)
		}
	}
}
