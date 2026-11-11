package querysession

import "flag"

func (c *Config) RegisterFlags(f *flag.FlagSet) {
	d := DefaultConfig()
	p := "query-frontend.metadata-streaming."
	f.DurationVar(&c.TTL, p+"session-ttl", d.TTL, "Metadata pagination session lifetime and connection idle timeout.")
	f.IntVar(&c.MaxFrameBytes, p+"max-frame-bytes", d.MaxFrameBytes, "Maximum uncompressed JSON result frame size.")
	f.Int64Var(&c.MaxTenantBytes, p+"max-tenant-bytes", d.MaxTenantBytes, "Per-tenant metadata session and dictionary memory budget.")
	f.Int64Var(&c.MaxProcessBytes, p+"max-process-bytes", d.MaxProcessBytes, "Process metadata session and dictionary memory budget.")
	f.IntVar(&c.MaxDictionaryBytes, p+"max-dictionary-bytes", d.MaxDictionaryBytes, "Per-connection string table memory budget.")
	f.IntVar(&c.MaxDictionaryEntries, p+"max-dictionary-entries", d.MaxDictionaryEntries, "Per-connection string table entry limit.")
	f.IntVar(&c.MaxActiveRequests, p+"max-active-requests", d.MaxActiveRequests, "Concurrent page requests per WebSocket.")
	f.IntVar(&c.MaxTenantSessions, p+"max-tenant-sessions", d.MaxTenantSessions, "Concurrent pagination sessions per tenant.")
	f.IntVar(&c.MaxSessions, p+"max-sessions", d.MaxSessions, "Concurrent pagination sessions per process.")
	f.IntVar(&c.MaxConnections, p+"max-connections", d.MaxConnections, "Concurrent metadata WebSockets per process.")
}
