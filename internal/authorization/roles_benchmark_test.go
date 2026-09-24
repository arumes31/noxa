package authorization

import "testing"

func BenchmarkRoleChannelResolution(b *testing.B) {
	evaluator, err := NewRoleEvaluator(roleFixture())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if !evaluator.Evaluate(2, 101, ViewChannel).Allowed {
			b.Fatal("moderator lost access to synced child")
		}
	}
}
