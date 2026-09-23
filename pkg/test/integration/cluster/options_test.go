package cluster

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
)

func TestV2ExternalMetastore(t *testing.T) {
	const address = "127.0.0.1:9095/metastore-2"
	c := NewMicroServiceCluster(WithV2(), WithTargets("query-frontend", "query-backend", "query-backend", "query-backend"), WithExternalMetastore(address))
	t.Cleanup(func() {
		if c.Directory() != "" {
			_ = os.RemoveAll(c.Directory())
		}
	})
	if err := c.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.MetastoreAddress() != address || len(c.perTarget["metastore"]) != 0 {
		t.Fatal("created a new metastore for queries")
	}
	for _, comp := range c.Components {
		if !slices.Contains(comp.flags, "-metastore.address="+address) {
			t.Fatalf("missing shared metastore address for %s", comp.Name())
		}
	}
}

func TestV2ExternalMetastoreRejectsWriteTargets(t *testing.T) {
	c := NewMicroServiceCluster(WithV2(), WithExternalMetastore("127.0.0.1:9095/metastore-2"))
	t.Cleanup(func() {
		if c.Directory() != "" {
			_ = os.RemoveAll(c.Directory())
		}
	})
	if err := c.Prepare(context.Background()); err == nil {
		t.Fatal("accepted write targets with external metastore")
	}
}

func TestV2CustomTopologyAndStorage(t *testing.T) {
	targets := []string{"distributor", "segment-writer", "segment-writer", "metastore", "metastore", "metastore", "query-frontend", "query-backend", "query-backend", "query-backend", "compaction-worker"}
	c := NewMicroServiceCluster(WithV2(), WithTargets(targets...), WithExtraFlags("-storage.backend=s3", "-storage.s3.endpoint=127.0.0.1:9000"))
	if err := c.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(c.Directory()); err != nil {
			t.Error(err)
		}
	})
	for target, want := range map[string]int{"segment-writer": 2, "query-backend": 3, "metastore": 3, "distributor": 1} {
		if got := len(c.perTarget[target]); got != want {
			t.Fatalf("%s replicas=%d, want %d", target, got, want)
		}
	}
	for _, comp := range c.Components {
		n := len(comp.flags)
		if n < 2 || comp.flags[n-2] != "-storage.backend=s3" || comp.flags[n-1] != "-storage.s3.endpoint=127.0.0.1:9000" {
			t.Fatalf("storage overrides missing from %s", comp.Name())
		}
		if comp.HTTPURL() == "http://127.0.0.1:0" {
			t.Fatal("HTTP port not allocated")
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Start(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled startup: %v", err)
	}
	// Cleanup must also be safe after Prepare or partial startup failure.
	if err := c.Stop()(context.Background()); err != nil {
		t.Fatal(err)
	}
}
