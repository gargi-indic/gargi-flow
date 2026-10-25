// Package config provides configuration definitions and loading functions.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// StoreCapabilitiesConfig specifies adapter selection per capability.
type StoreCapabilitiesConfig struct {
	State        string `yaml:"state,omitempty"`
	Lock         string `yaml:"lock,omitempty"`
	Events       string `yaml:"events,omitempty"`
	ExecutionLog string `yaml:"execution_log,omitempty"`
	// Experiments selects the simulation experiment store (models,
	// policies, experiments and reports): memory or postgres. Empty
	// selects postgres when Driver is postgres, otherwise memory.
	Experiments string `yaml:"experiments,omitempty"`
}

// StoreConfig holds the configuration for the storage adapter.
type StoreConfig struct {
	Driver       string                  `yaml:"driver"`
	Capabilities StoreCapabilitiesConfig `yaml:"capabilities,omitempty"`
	Redis        *RedisConfig            `yaml:"redis,omitempty"`
	Postgres     *PostgresConfig         `yaml:"postgres,omitempty"`
	Neo4j        *Neo4jConfig            `yaml:"neo4j,omitempty"`
}

// EffectiveStateDriver returns the driver configured for state persistence.
// Falls back to Driver if not explicitly configured in Capabilities.
func (s StoreConfig) EffectiveStateDriver() string {
	if s.Capabilities.State != "" {
		return s.Capabilities.State
	}
	if s.Driver != "" {
		return s.Driver
	}
	return "memory"
}

// EffectiveLockDriver returns the driver configured for distributed locks.
// Falls back to Driver if not explicitly configured and Driver supports locking.
func (s StoreConfig) EffectiveLockDriver() string {
	if s.Capabilities.Lock != "" {
		return s.Capabilities.Lock
	}
	// neo4j does not implement LockManager, so don't default lock to neo4j
	if s.Driver == "neo4j" {
		return ""
	}
	return s.Driver
}

// EffectiveEventsDriver returns the driver configured for event publishing.
// Falls back to Driver if not explicitly configured and Driver supports events.
func (s StoreConfig) EffectiveEventsDriver() string {
	if s.Capabilities.Events != "" {
		return s.Capabilities.Events
	}
	// neo4j does not implement EventPublisher, so don't default events to neo4j
	if s.Driver == "neo4j" {
		return ""
	}
	return s.Driver
}

// EffectiveExecutionLogDriver returns the driver configured for execution audit logging.
// Falls back to Driver if not explicitly configured.
func (s StoreConfig) EffectiveExecutionLogDriver() string {
	if s.Capabilities.ExecutionLog != "" {
		return s.Capabilities.ExecutionLog
	}
	return s.Driver
}

// EffectiveExperimentsDriver returns the driver configured for the
// simulation experiment store. Falls back to postgres when Driver is
// postgres and to memory otherwise (redis and neo4j do not implement it).
func (s StoreConfig) EffectiveExperimentsDriver() string {
	if s.Capabilities.Experiments != "" {
		return s.Capabilities.Experiments
	}
	if s.Driver == "postgres" {
		return "postgres"
	}
	return "memory"
}

// EffectivePoliciesDriver returns the driver configured for the policy store.
// Falls back to postgres when Driver is postgres, and memory otherwise.
func (s StoreConfig) EffectivePoliciesDriver() string {
	if s.Driver == "postgres" {
		return "postgres"
	}
	return "memory"
}

// DriversInUse returns a deduplicated list of drivers required across all configured capabilities.
func (s StoreConfig) DriversInUse() []string {
	seen := make(map[string]bool)
	var drivers []string

	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			drivers = append(drivers, d)
		}
	}

	add(s.EffectiveStateDriver())
	add(s.EffectiveLockDriver())
	add(s.EffectiveEventsDriver())
	add(s.EffectiveExecutionLogDriver())
	add(s.EffectiveExperimentsDriver())

	return drivers
}

// RedisConfig holds connection settings for Redis.
type RedisConfig struct {
	Addr string `yaml:"addr"`
}

// PostgresConfig holds connection settings for Postgres.
type PostgresConfig struct {
	DSN string `yaml:"dsn"`
}

// Neo4jConfig holds connection settings for Neo4j.
type Neo4jConfig struct {
	URI      string `yaml:"uri"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

// TLSConfig holds optional TLS settings for the Temporal client connection.
type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`
	CAFile   string `yaml:"ca_file,omitempty"`
}

// TemporalConfig holds the configuration for the Temporal connection.
type TemporalConfig struct {
	HostPort                string     `yaml:"host_port"`
	Namespace               string     `yaml:"namespace"`
	TaskQueue               string     `yaml:"task_queue"`
	WorkflowTaskQueue       string     `yaml:"workflow_task_queue,omitempty"`
	ActivityTaskQueue       string     `yaml:"activity_task_queue,omitempty"`
	TLS                     *TLSConfig `yaml:"tls,omitempty"`
	BuildID                 string     `yaml:"build_id,omitempty"`
	UseBuildIDForVersioning bool       `yaml:"use_build_id_for_versioning,omitempty"`
	TracingEnabled          bool       `yaml:"tracing_enabled,omitempty"`
}

// EffectiveWorkflowTaskQueue returns WorkflowTaskQueue if non-empty, otherwise TaskQueue.
func (t TemporalConfig) EffectiveWorkflowTaskQueue() string {
	if t.WorkflowTaskQueue != "" {
		return t.WorkflowTaskQueue
	}
	return t.TaskQueue
}

// EffectiveActivityTaskQueue returns ActivityTaskQueue if non-empty, otherwise TaskQueue.
func (t TemporalConfig) EffectiveActivityTaskQueue() string {
	if t.ActivityTaskQueue != "" {
		return t.ActivityTaskQueue
	}
	return t.TaskQueue
}

// CORSConfig holds CORS configuration for the HTTP server.
type CORSConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins,omitempty"`
}

// HTTPConfig holds the configuration for the HTTP server.
type HTTPConfig struct {
	Address string     `yaml:"address,omitempty"` // For backward compatibility if needed, but we keep HTTPAddress
	CORS    CORSConfig `yaml:"cors,omitempty"`
}

// ServerConfig holds the configuration for the API server.
type ServerConfig struct {
	HTTPAddress string     `yaml:"http_address"`
	GRPCAddress string     `yaml:"grpc_address"`
	HTTP        HTTPConfig `yaml:"http,omitempty"`
}

// LoggingConfig holds configuration for application logging.
type LoggingConfig struct {
	Level  string `yaml:"level,omitempty"`
	Format string `yaml:"format,omitempty"`
}

// TracingConfig holds configuration for OpenTelemetry distributed tracing.
type TracingConfig struct {
	Enabled      bool    `yaml:"enabled"`
	Endpoint     string  `yaml:"endpoint"`
	Insecure     bool    `yaml:"insecure"`
	SamplerRatio float64 `yaml:"sampler_ratio"`
	ServiceName  string  `yaml:"service_name,omitempty"`
}

// Config represents the root configuration structure.
type Config struct {
	Store    StoreConfig    `yaml:"store"`
	Temporal TemporalConfig `yaml:"temporal"`
	Server   ServerConfig   `yaml:"server"`
	Logging  LoggingConfig  `yaml:"logging,omitempty"`
	Tracing  TracingConfig  `yaml:"tracing,omitempty"`
}

// Default returns a new configuration instance with default values populated.
func Default() *Config {
	return &Config{
		Store: StoreConfig{
			Driver: "memory",
		},
		Temporal: TemporalConfig{
			HostPort:  "localhost:7233",
			Namespace: "default",
			TaskQueue: "gargi_tasks",
		},
		Server: ServerConfig{
			HTTPAddress: ":8080",
			GRPCAddress: ":9090",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "text",
		},
		Tracing: TracingConfig{
			Enabled:      false,
			Endpoint:     "",
			Insecure:     true,
			SamplerRatio: 1.0,
			ServiceName:  "gargi-flow",
		},
	}
}

// Load reads the configuration from a YAML file (if path is provided)
// and applies environment variable overrides on top of defaults.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}

		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)

		if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	applyEnvOverrides(cfg)

	return cfg, nil
}

// applyEnvOverrides updates configuration based on environment variables.
func applyEnvOverrides(cfg *Config) {
	if val, ok := os.LookupEnv("GARGI_STORE_DRIVER"); ok {
		cfg.Store.Driver = val
	}
	if val, ok := os.LookupEnv("GARGI_STORE_REDIS_ADDR"); ok {
		if cfg.Store.Redis == nil {
			cfg.Store.Redis = &RedisConfig{}
		}
		cfg.Store.Redis.Addr = val
	}
	if val, ok := os.LookupEnv("GARGI_STORE_POSTGRES_DSN"); ok {
		if cfg.Store.Postgres == nil {
			cfg.Store.Postgres = &PostgresConfig{}
		}
		cfg.Store.Postgres.DSN = val
	}
	if val, ok := os.LookupEnv("GARGI_STORE_NEO4J_URI"); ok {
		if cfg.Store.Neo4j == nil {
			cfg.Store.Neo4j = &Neo4jConfig{}
		}
		cfg.Store.Neo4j.URI = val
	}
	if val, ok := os.LookupEnv("GARGI_STORE_NEO4J_USER"); ok {
		if cfg.Store.Neo4j == nil {
			cfg.Store.Neo4j = &Neo4jConfig{}
		}
		cfg.Store.Neo4j.User = val
	}
	if val, ok := os.LookupEnv("GARGI_STORE_NEO4J_PASSWORD"); ok {
		if cfg.Store.Neo4j == nil {
			cfg.Store.Neo4j = &Neo4jConfig{}
		}
		cfg.Store.Neo4j.Password = val
	}

	if val, ok := os.LookupEnv("GARGI_STORE_CAPABILITIES_STATE"); ok {
		cfg.Store.Capabilities.State = val
	} else if val, ok := os.LookupEnv("GARGI_STORE_STATE_DRIVER"); ok {
		cfg.Store.Capabilities.State = val
	}
	if val, ok := os.LookupEnv("GARGI_STORE_CAPABILITIES_LOCK"); ok {
		cfg.Store.Capabilities.Lock = val
	} else if val, ok := os.LookupEnv("GARGI_STORE_LOCK_DRIVER"); ok {
		cfg.Store.Capabilities.Lock = val
	}
	if val, ok := os.LookupEnv("GARGI_STORE_CAPABILITIES_EVENTS"); ok {
		cfg.Store.Capabilities.Events = val
	} else if val, ok := os.LookupEnv("GARGI_STORE_EVENTS_DRIVER"); ok {
		cfg.Store.Capabilities.Events = val
	}
	if val, ok := os.LookupEnv("GARGI_STORE_CAPABILITIES_EXECUTION_LOG"); ok {
		cfg.Store.Capabilities.ExecutionLog = val
	} else if val, ok := os.LookupEnv("GARGI_STORE_EXECUTION_LOG_DRIVER"); ok {
		cfg.Store.Capabilities.ExecutionLog = val
	}
	if val, ok := os.LookupEnv("GARGI_STORE_CAPABILITIES_EXPERIMENTS"); ok {
		cfg.Store.Capabilities.Experiments = val
	}

	if val, ok := os.LookupEnv("GARGI_TEMPORAL_HOST_PORT"); ok {
		cfg.Temporal.HostPort = val
	}
	if val, ok := os.LookupEnv("GARGI_TEMPORAL_NAMESPACE"); ok {
		cfg.Temporal.Namespace = val
	}
	if val, ok := os.LookupEnv("GARGI_TEMPORAL_TASK_QUEUE"); ok {
		cfg.Temporal.TaskQueue = val
	}
	if val, ok := os.LookupEnv("GARGI_TEMPORAL_WORKFLOW_TASK_QUEUE"); ok {
		cfg.Temporal.WorkflowTaskQueue = val
	}
	if val, ok := os.LookupEnv("GARGI_TEMPORAL_ACTIVITY_TASK_QUEUE"); ok {
		cfg.Temporal.ActivityTaskQueue = val
	}
	if val, ok := os.LookupEnv("GARGI_TEMPORAL_TLS_ENABLED"); ok {
		if cfg.Temporal.TLS == nil {
			cfg.Temporal.TLS = &TLSConfig{}
		}
		cfg.Temporal.TLS.Enabled = val == "true" || val == "1"
	}
	if val, ok := os.LookupEnv("GARGI_TEMPORAL_TLS_CERT_FILE"); ok {
		if cfg.Temporal.TLS == nil {
			cfg.Temporal.TLS = &TLSConfig{}
		}
		cfg.Temporal.TLS.CertFile = val
	}
	if val, ok := os.LookupEnv("GARGI_TEMPORAL_TLS_KEY_FILE"); ok {
		if cfg.Temporal.TLS == nil {
			cfg.Temporal.TLS = &TLSConfig{}
		}
		cfg.Temporal.TLS.KeyFile = val
	}
	if val, ok := os.LookupEnv("GARGI_TEMPORAL_TLS_CA_FILE"); ok {
		if cfg.Temporal.TLS == nil {
			cfg.Temporal.TLS = &TLSConfig{}
		}
		cfg.Temporal.TLS.CAFile = val
	}
	if val, ok := os.LookupEnv("GARGI_TEMPORAL_BUILD_ID"); ok {
		cfg.Temporal.BuildID = val
	}
	if val, ok := os.LookupEnv("GARGI_TEMPORAL_USE_BUILD_ID_FOR_VERSIONING"); ok {
		cfg.Temporal.UseBuildIDForVersioning = val == "true" || val == "1"
	}
	if val, ok := os.LookupEnv("GARGI_TEMPORAL_TRACING_ENABLED"); ok {
		cfg.Temporal.TracingEnabled = val == "true" || val == "1"
	}

	if val, ok := os.LookupEnv("GARGI_SERVER_HTTP_ADDRESS"); ok {
		cfg.Server.HTTPAddress = val
	}
	if val, ok := os.LookupEnv("GARGI_SERVER_GRPC_ADDRESS"); ok {
		cfg.Server.GRPCAddress = val
	}
	if val, ok := os.LookupEnv("GARGI_HTTP_CORS_ALLOWED_ORIGINS"); ok {
		origins := strings.Split(val, ",")
		for i := range origins {
			origins[i] = strings.TrimSpace(origins[i])
		}
		cfg.Server.HTTP.CORS.AllowedOrigins = origins
	}

	if val, ok := os.LookupEnv("GARGI_LOG_LEVEL"); ok {
		cfg.Logging.Level = val
	}
	if val, ok := os.LookupEnv("GARGI_LOG_FORMAT"); ok {
		cfg.Logging.Format = val
	}

	if val, ok := os.LookupEnv("GARGI_TRACING_ENABLED"); ok {
		cfg.Tracing.Enabled = val == "true" || val == "1"
	}
	if val, ok := os.LookupEnv("GARGI_TRACING_ENDPOINT"); ok {
		cfg.Tracing.Endpoint = val
		if _, set := os.LookupEnv("GARGI_TRACING_ENABLED"); !set && val != "" {
			cfg.Tracing.Enabled = true
		}
	}
	if val, ok := os.LookupEnv("GARGI_TRACING_INSECURE"); ok {
		cfg.Tracing.Insecure = val == "true" || val == "1"
	}
	if val, ok := os.LookupEnv("GARGI_TRACING_SAMPLER_RATIO"); ok {
		if r, err := strconv.ParseFloat(val, 64); err == nil {
			cfg.Tracing.SamplerRatio = r
		}
	}
	if val, ok := os.LookupEnv("GARGI_TRACING_SERVICE_NAME"); ok {
		cfg.Tracing.ServiceName = val
	}
	if cfg.Tracing.Enabled && !cfg.Temporal.TracingEnabled {
		if _, set := os.LookupEnv("GARGI_TEMPORAL_TRACING_ENABLED"); !set {
			cfg.Temporal.TracingEnabled = true
		}
	}
}

// Validate checks the configuration for required fields and valid options.
func (c *Config) Validate() error {
	driver := c.Store.Driver
	if driver != "" {
		switch driver {
		case "memory", "redis", "postgres", "neo4j":
			// valid
		default:
			return fmt.Errorf("invalid store driver: %s. Allowed values: memory, redis, postgres, neo4j", c.Store.Driver)
		}
	}

	stateDriver := c.Store.EffectiveStateDriver()
	switch stateDriver {
	case "memory", "redis", "postgres", "neo4j":
		// valid
	default:
		return fmt.Errorf("invalid store state capability driver: %s. Allowed values: memory, redis, postgres, neo4j", stateDriver)
	}

	lockDriver := c.Store.EffectiveLockDriver()
	if lockDriver != "" {
		switch lockDriver {
		case "memory", "redis", "postgres":
			// valid
		case "neo4j":
			return errors.New("neo4j does not support capability 'lock' (allowed: memory, redis, postgres)")
		default:
			return fmt.Errorf("invalid store lock capability driver: %s. Allowed values: memory, redis, postgres", lockDriver)
		}
	}

	eventsDriver := c.Store.EffectiveEventsDriver()
	if eventsDriver != "" {
		switch eventsDriver {
		case "memory", "redis", "postgres":
			// valid
		case "neo4j":
			return errors.New("neo4j does not support capability 'events' (allowed: memory, redis, postgres)")
		default:
			return fmt.Errorf("invalid store events capability driver: %s. Allowed values: memory, redis, postgres", eventsDriver)
		}
	}

	logDriver := c.Store.EffectiveExecutionLogDriver()
	if logDriver != "" {
		switch logDriver {
		case "memory", "redis", "postgres", "neo4j":
			// valid
		default:
			return fmt.Errorf("invalid store execution_log capability driver: %s. Allowed values: memory, redis, postgres, neo4j", logDriver)
		}
	}

	switch experimentsDriver := c.Store.EffectiveExperimentsDriver(); experimentsDriver {
	case "memory", "postgres":
		// valid
	case "redis", "neo4j":
		return fmt.Errorf("%s does not support capability 'experiments' (allowed: memory, postgres)", experimentsDriver)
	default:
		return fmt.Errorf("invalid store experiments capability driver: %s. Allowed values: memory, postgres", experimentsDriver)
	}

	for _, d := range c.Store.DriversInUse() {
		switch d {
		case "redis":
			if c.Store.Redis == nil || c.Store.Redis.Addr == "" {
				return errors.New("redis configuration requires addr")
			}
		case "postgres":
			if c.Store.Postgres == nil || c.Store.Postgres.DSN == "" {
				return errors.New("postgres configuration requires dsn")
			}
		case "neo4j":
			if c.Store.Neo4j == nil || c.Store.Neo4j.URI == "" || c.Store.Neo4j.User == "" || c.Store.Neo4j.Password == "" {
				return errors.New("neo4j configuration requires uri, user, and password")
			}
		}
	}

	if c.Temporal.HostPort == "" {
		return errors.New("temporal host_port is required")
	}
	if c.Temporal.Namespace == "" {
		return errors.New("temporal namespace is required")
	}
	if c.Temporal.EffectiveWorkflowTaskQueue() == "" || c.Temporal.EffectiveActivityTaskQueue() == "" {
		return errors.New("temporal task_queue (or both workflow_task_queue and activity_task_queue) is required")
	}

	if c.Temporal.TLS != nil && c.Temporal.TLS.Enabled {
		if (c.Temporal.TLS.CertFile == "") != (c.Temporal.TLS.KeyFile == "") {
			return errors.New("temporal tls requires both cert_file and key_file when client certificate is configured")
		}
	}

	if c.Temporal.UseBuildIDForVersioning && c.Temporal.BuildID == "" {
		return errors.New("temporal build_id is required when use_build_id_for_versioning is enabled")
	}

	if c.Server.HTTPAddress == "" {
		return errors.New("server http_address is required")
	}
	if c.Server.GRPCAddress == "" {
		return errors.New("server grpc_address is required")
	}

	return nil
}
