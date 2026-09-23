package cluster

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Raft peers persist advertised addresses. Reusing ports is as important as
// retaining the WAL and snapshots when restarting a metastore cluster.
func (c *Cluster) componentPorts(perComponent int) ([]int, error) {
	count := len(c.Components) * perComponent
	if c.persistentDir == "" {
		return getFreeTCPPorts(listenAddr, count)
	}
	path := filepath.Join(c.tmpDir, "ports.json")
	var saved struct {
		Targets []string
		Ports   []int
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &saved); err != nil {
			return nil, err
		}
		if !slices.Equal(saved.Targets, c.expectedComponents) || len(saved.Ports) != count {
			return nil, fmt.Errorf("persistent cluster topology differs from saved ports")
		}
		seen := map[int]bool{}
		for _, port := range saved.Ports {
			if port < 1 || port > 65535 || seen[port] {
				return nil, fmt.Errorf("invalid persistent cluster ports")
			}
			seen[port] = true
		}
		return saved.Ports, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	saved.Targets = c.expectedComponents
	saved.Ports, err = getFreeTCPPorts(listenAddr, count)
	if err != nil {
		return nil, err
	}
	data, err = json.Marshal(saved)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		return nil, err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return nil, err
	}
	return saved.Ports, nil
}
