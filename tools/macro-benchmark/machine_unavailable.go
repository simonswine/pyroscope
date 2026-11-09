package main

// stopUnavailableMachineJobs reconciles durable checkpoints after EC2 confirms
// the host is gone. Retain infrastructure IDs for subsequent resource cleanup.
func (s *stateStore) stopUnavailableMachineJobs(selected *sessionState) error {
	machineID := selected.MachineID
	if selected.Kind == "machine" {
		machineID = selected.Config.RunID
	}
	states, err := s.list()
	if err != nil {
		return err
	}
	for i := range states {
		st := &states[i]
		sameHost := machineID != "" && st.MachineID == machineID
		if machineID == "" {
			sameHost = selected.InstanceID != "" && st.InstanceID == selected.InstanceID
		}
		if !sameHost {
			continue
		}
		switch st.Phase {
		case "completed", "failed", "stopped", "interrupted", "destroyed":
			continue
		}
		st.Phase, st.Error, st.Progress = "stopped", errInstanceUnavailable.Error(), nil
		if err := s.save(st); err != nil {
			return err
		}
		if st.Config.RunID == selected.Config.RunID {
			*selected = *st
		}
	}
	if machineID != "" {
		m, err := s.loadMachine(machineID)
		if err != nil {
			return err
		}
		if m.Phase != "destroyed" {
			m.Phase, m.Error, m.Progress = "interrupted", errInstanceUnavailable.Error(), nil
			if err := s.save(&m); err != nil {
				return err
			}
			if selected.Kind == "machine" {
				*selected = m
			}
		}
	}
	return nil
}
