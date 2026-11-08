package admission

import (
	"fmt"
	"math"
	"testing"

	"github.com/DiyazY/di-agent/pkg/statemap"
)

type admissionComparisonCase struct {
	name           string
	usage          float64
	pressure       float64
	demand         float64
	measuredEffect float64
	postPressure   float64
	wantBaseline   bool
	wantPrediction bool
}

type admissionComparisonScore struct {
	falseAccepts        int
	unnecessaryDeclines int
}

// These controlled cases isolate pressure prediction from the shared headroom
// gate. postPressure is the specified outcome if the offer runs, not a live-data
// estimate; this is a regression comparison, not empirical performance evidence.
func TestPredictionAgainstHeadroomBaselineControlledScenarios(t *testing.T) {
	cases := []admissionComparisonCase{
		{name: "headroom still prevents oversubscription", usage: 0.8, pressure: 0, demand: 0.8, postPressure: 0.3},
		{name: "prediction catches first pressure increase", usage: 0.4, pressure: 0.09, demand: 1, measuredEffect: 0.3, postPressure: 0.14, wantBaseline: true},
		{name: "prediction catches second pressure increase", usage: 0.4, pressure: 0.07, demand: 0.5, measuredEffect: 0.9, postPressure: 0.12, wantBaseline: true},
		{name: "safe offer remains accepted", usage: 0.4, pressure: 0.01, demand: 0.2, measuredEffect: 0.2, postPressure: 0.04, wantBaseline: true, wantPrediction: true},
		{name: "overestimated effect costs a safe offer", usage: 0.4, pressure: 0.05, demand: 1, measuredEffect: 0.9, postPressure: 0.06, wantBaseline: true},
	}

	var baseline, predicted admissionComparisonScore
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := node(t, tc.usage, tc.pressure)
			if tc.measuredEffect > 0 {
				effect(t, m, "pod:a", tc.measuredEffect)
			}
			o := offer(tc.demand)
			gate := m.Admit(statemap.AdmitRequest{
				ID: "baseline-" + tc.name, CPUCores: tc.demand, MemoryBytes: o.Demand["memory"].Value,
				CPUEnter: cpuDom.Enter, MemoryEnter: memDom.Enter,
			})
			if gate.Err != "" {
				t.Fatalf("headroom-only gate error: %s", gate.Err)
			}
			if gate.Accepted != tc.wantBaseline {
				t.Fatalf("headroom-only gate accepted=%t; want %t", gate.Accepted, tc.wantBaseline)
			}
			got := Decide(m, o, both, self, "candidate-"+tc.name)

			baselineAccepted := gate.Accepted
			predictedAccepted := got.Verdict == Accept
			safe := tc.postPressure <= cpuDom.Exit
			scoreAdmission(&baseline, baselineAccepted, safe)
			scoreAdmission(&predicted, predictedAccepted, safe)

			wantVerdict := Decline
			if tc.wantPrediction {
				wantVerdict = Accept
			}
			if got.Verdict != wantVerdict {
				t.Fatalf("candidate verdict %s; expected %s (reason %q)", got.Verdict, wantVerdict, got.Reason)
			}
			t.Logf("outcome_safe=%t headroom_only=%s prediction=%s post_pressure=%.3f",
				safe, verdict(baselineAccepted), got.Verdict, tc.postPressure)
		})
	}

	if baseline.falseAccepts != 2 || baseline.unnecessaryDeclines != 0 {
		t.Fatalf("headroom-only score %+v; want 2 false accepts, 0 unnecessary declines", baseline)
	}
	if predicted.falseAccepts != 0 || predicted.unnecessaryDeclines != 1 {
		t.Fatalf("prediction score %+v; want 0 false accepts, 1 unnecessary decline", predicted)
	}
	t.Logf("summary: headroom-only %+v; prediction %+v", baseline, predicted)
}

// TestPredictionReplayAgainstHeadroomBaseline replays offers against two known
// pressure curves. The linear and logistic curves mirror the scripted scenario
// model; every post-admission value is computed from its curve, not hand-labeled.
func TestPredictionReplayAgainstHeadroomBaseline(t *testing.T) {
	tests := []struct {
		name           string
		curve          func(float64) float64
		slope          func(float64) float64
		wantBaseline   admissionComparisonScore
		wantPrediction admissionComparisonScore
	}{
		{
			name: "linear",
			curve: func(usage float64) float64 {
				return 0.01 + 0.10*usage
			},
			slope:          func(float64) float64 { return 0.10 },
			wantBaseline:   admissionComparisonScore{},
			wantPrediction: admissionComparisonScore{},
		},
		{
			name: "saturation",
			curve: func(usage float64) float64 {
				return logistic((usage - 0.85) / 0.05)
			},
			slope: func(usage float64) float64 {
				p := logistic((usage - 0.85) / 0.05)
				return math.Min(1, p*(1-p)/0.05)
			},
			wantBaseline:   admissionComparisonScore{falseAccepts: 5},
			wantPrediction: admissionComparisonScore{falseAccepts: 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var baseline, predicted admissionComparisonScore
			offers := 0
			for _, usage := range []float64{0.20, 0.35, 0.50, 0.65, 0.75, 0.82} {
				for _, share := range []float64{0.05, 0.10, 0.20, 0.30} {
					if usage+share > 0.90 {
						continue
					}
					offers++
					m, _ := node(t, usage, tt.curve(usage))
					for i := 0; i < 8; i++ {
						effect(t, m, fmt.Sprintf("pod:calibration-%d", i), tt.slope(usage))
					}

					o := Offer{Name: "replay", Demand: map[string]Demand{
						"cpu": {Value: share * 4, Source: "declared"},
					}}
					gate := m.Admit(statemap.AdmitRequest{
						ID:       fmt.Sprintf("baseline-%s-%d-%d", tt.name, offers, int(usage*100)),
						CPUCores: share * 4, CPUEnter: cpuDom.Enter,
					})
					if gate.Err != "" {
						t.Fatalf("headroom-only gate error at usage %.2f share %.2f: %s", usage, share, gate.Err)
					}
					got := Decide(m, o, []Domain{cpuDom}, self,
						fmt.Sprintf("candidate-%s-%d-%d", tt.name, offers, int(usage*100)))
					safe := tt.curve(usage+share) <= cpuDom.Exit
					scoreAdmission(&baseline, gate.Accepted, safe)
					scoreAdmission(&predicted, got.Verdict == Accept, safe)
				}
			}
			if offers == 0 {
				t.Fatal("no offers replayed")
			}
			if baseline != tt.wantBaseline || predicted != tt.wantPrediction {
				t.Fatalf("replayed %d offers: headroom-only %+v (want %+v), prediction %+v (want %+v)",
					offers, baseline, tt.wantBaseline, predicted, tt.wantPrediction)
			}
			t.Logf("%d offers: headroom-only false_accepts=%d unnecessary_declines=%d; prediction false_accepts=%d unnecessary_declines=%d",
				offers, baseline.falseAccepts, baseline.unnecessaryDeclines,
				predicted.falseAccepts, predicted.unnecessaryDeclines)
		})
	}
}

func TestCalmMemoryHeadroomReplayAgainstBaseline(t *testing.T) {
	var baseline, predicted admissionComparisonScore
	offers := 0
	for _, headroom := range []float64{0.05, 0.10, 0.20} {
		for _, share := range []float64{0.05, 0.10, 0.20, 0.30} {
			offers++
			m, _ := node(t, 0.4, 0.02)
			record(t, m, "memory_admission_headroom_ratio", "", headroom)
			o := Offer{Name: "memory-replay", Demand: map[string]Demand{
				"memory": {Value: share * (8 << 30), Source: "declared"},
			}}
			gate := m.Admit(statemap.AdmitRequest{
				ID: fmt.Sprintf("memory-baseline-%d", offers), MemoryBytes: o.Demand["memory"].Value,
				MemoryEnter: memDom.Enter,
			})
			if gate.Err != "" {
				t.Fatalf("headroom-only gate error for offer share %.2f: %s", share, gate.Err)
			}
			got := Decide(m, o, []Domain{memDom}, self, fmt.Sprintf("memory-candidate-%d", offers))
			safe := share <= headroom
			scoreAdmission(&baseline, gate.Accepted, safe)
			scoreAdmission(&predicted, got.Verdict == Accept, safe)
		}
	}
	if baseline != (admissionComparisonScore{falseAccepts: 6}) {
		t.Fatalf("%d memory offers: headroom-only score %+v; want 6 false accepts", offers, baseline)
	}
	if predicted != (admissionComparisonScore{}) {
		t.Fatalf("%d memory offers: prediction score %+v; want no false accepts or unnecessary declines", offers, predicted)
	}
	t.Logf("%d calm-memory offers: headroom-only false_accepts=%d unnecessary_declines=%d; prediction false_accepts=%d unnecessary_declines=%d",
		offers, baseline.falseAccepts, baseline.unnecessaryDeclines,
		predicted.falseAccepts, predicted.unnecessaryDeclines)
}

func logistic(x float64) float64 {
	return 1 / (1 + math.Exp(-x))
}

func scoreAdmission(score *admissionComparisonScore, accepted, safe bool) {
	switch {
	case accepted && !safe:
		score.falseAccepts++
	case !accepted && safe:
		score.unnecessaryDeclines++
	}
}

func verdict(accepted bool) string {
	if accepted {
		return Accept
	}
	return Decline
}
