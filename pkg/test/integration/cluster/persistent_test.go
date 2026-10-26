package cluster

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPersistentClusterRestart(t *testing.T) {
	dir := t.TempDir()
	newCluster := func() *Cluster {
		return NewMicroServiceCluster(WithV2(), WithTargets("metastore", "metastore", "metastore"), WithDirectory(dir))
	}
	first := newCluster()
	if err := first.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(first.dataDir(first.Components[0]), "persisted-state")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	second := newCluster()
	if err := second.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if second.MetastoreAddress() != first.MetastoreAddress() {
		t.Fatal("raft leader address changed on restart")
	}
	for i, comp := range first.Components {
		if !slices.Equal(comp.flags, second.Components[i].flags) {
			t.Fatal("component addresses or data paths changed on restart")
		}
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "keep" {
		t.Fatal("persisted state was lost")
	}
	other := NewMicroServiceCluster(WithV2(), WithTargets("metastore"), WithDirectory(dir))
	if err := other.Prepare(context.Background()); err == nil {
		t.Fatal("accepted incompatible persistent topology")
	}
}

func TestPersistentClusterRejectsCorruptPorts(t *testing.T) {
	for _, content := range []string{`invalid`, `{"Targets":["metastore"],"Ports":[1,1,2,3]}`, `{"Targets":["metastore"],"Ports":[0,2,3,4]}`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ports.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		c := NewMicroServiceCluster(WithV2(), WithTargets("metastore"), WithDirectory(dir))
		if err := c.Prepare(context.Background()); err == nil {
			t.Fatalf("accepted %s", content)
		}
	}
}
