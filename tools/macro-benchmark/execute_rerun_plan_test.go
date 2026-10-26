package main

import "testing"

func TestRerunStoragePlanAllowsOnlyReadChanges(t *testing.T) {
	owner := runPlan{Ingest: targetRevision{Ref: "ingest", Commit: "fixed"}, Datasets: []datasetReplay{{Name: "full-tenant", Tenant: "original"}}}
	for _, test := range []struct {
		name      string
		change    func(*runPlan)
		wantError bool
	}{
		{"read revisions", func(p *runPlan) {
			p.Baseline = targetRevision{Ref: "new-base", Commit: "a"}
			p.Comparison = targetRevision{Ref: "new-comparison", Commit: "b"}
		}, false},
		{"ingest", func(p *runPlan) { p.Ingest.Commit = "changed" }, true},
		{"tenant", func(p *runPlan) { p.Datasets[0].Tenant = "changed" }, true},
		{"new dataset", func(p *runPlan) { p.Datasets[0].Name = "new" }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			child := owner
			child.Datasets = append([]datasetReplay(nil), owner.Datasets...)
			test.change(&child)
			if err := validateRerunStoragePlan(owner, child); (err != nil) != test.wantError {
				t.Fatalf("unexpected validation result: %v", err)
			}
		})
	}
}
