package processsupervisor

import (
	"testing"
	"time"
)

func pressureSample(seconds int, full, cpu uint64) MemoryPressureSample {
	return MemoryPressureSample{At: time.Unix(int64(seconds+100), 0), Cgroup: "/owned",
		MemoryPressure: MemoryPressure{Status: "pressured", CurrentBytes: 95, HighBytes: 80, LimitBytes: 100, FullStallPercent: 90},
		FullStallUsec:  full, UserCPUUsec: cpu}
}

func TestMemoryPressurePolicyRelievesBeforeRecovering(t *testing.T) {
	p := NewMemoryPressurePolicy(10*time.Second, 30*time.Second)
	p.Observe(pressureSample(0, 0, 0))
	for i := 1; i <= 4; i++ {
		s := pressureSample(i*10, uint64(i)*9_000_000, 0)
		if i > 1 {
			s.HighBytes = 100
		}
		want := MemoryPressureObserve
		if i == 1 {
			want = MemoryPressureRelieve
		}
		if i == 4 {
			want = MemoryPressureRecover
		}
		if got := p.Observe(s); got != want {
			t.Fatalf("sample %d action=%d want=%d", i, got, want)
		}
	}
	if got := p.Observe(pressureSample(50, 45_000_000, 0)); got != MemoryPressureObserve {
		t.Fatal("recovery repeated")
	}
}

func TestMemoryPressurePolicyNeverUsesJobAgeAsDeadline(t *testing.T) {
	for _, mode := range []string{"productive", "output-progress", "healthy", "unbounded", "unavailable", "counter-reset", "sample-gap"} {
		t.Run(mode, func(t *testing.T) {
			p := NewMemoryPressurePolicy(time.Second, 2*time.Second)
			for i := 0; i < 1000; i++ {
				s := pressureSample(i*10, uint64(i)*9_000_000, 0)
				switch mode {
				case "output-progress":
					s.ProgressSequence = uint64(i)
				case "productive":
					s.UserCPUUsec = uint64(i) * 8_000_000
				case "healthy":
					s.FullStallUsec = uint64(i) * 1000
				case "unbounded":
					s.LimitBytes = 0
				case "unavailable":
					s.Status = "unavailable"
				case "counter-reset":
					s.FullStallUsec = 1_000_000 - uint64(i)
				case "sample-gap":
					s.At = time.Unix(int64(i*60), 0)
				}
				if action := p.Observe(s); action != MemoryPressureObserve {
					t.Fatalf("healthy/unknown job got action %d", action)
				}
			}
		})
	}
}

func TestMemoryPressurePolicyResetsAfterTransientRecovery(t *testing.T) {
	p := NewMemoryPressurePolicy(30*time.Second, time.Minute)
	p.Observe(pressureSample(0, 0, 0))
	p.Observe(pressureSample(10, 9_000_000, 0))
	p.Observe(pressureSample(20, 18_000_000, 0))
	p.Observe(pressureSample(30, 18_000_000, 0))
	if action := p.Observe(pressureSample(40, 27_000_000, 0)); action != MemoryPressureObserve {
		t.Fatal("temporary pressure consumed recovery window")
	}
}

func TestMemoryPressurePolicyCorroboratesKernelAccountingSkew(t *testing.T) {
	for _, average := range []float64{95, 0} {
		policy := NewMemoryPressurePolicy(30*time.Second, time.Minute)
		var action MemoryPressureAction
		for i := 0; i <= 7; i++ {
			sample := pressureSample(i*5, uint64(i)*5_450_000, 0)
			sample.FullStallPercent = average
			action = policy.Observe(sample)
			if average == 0 && action != MemoryPressureObserve {
				t.Fatal("uncorroborated counter anomaly caused intervention")
			}
			if average > 0 && i == 6 && action != MemoryPressureRelieve {
				t.Fatal("corroborated pressure was discarded due to accounting skew")
			}
		}
	}
}
