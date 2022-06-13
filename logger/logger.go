package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Key int

const (
	AccessKey Key = iota
	AdminKey
	AuthKey
	BucketKey // what does this even mean?
	CacheKey
	ChannelKey
	ChangesKey
	ClusterKey
	ConfigKey
	CRUDKey // TODO should this be merged with a different key?
	DCPKey
	EventsKey
	HTTPKey
	ImportKey
	JavascriptKey
	MigrateKey
	QueryKey
	ReplicateKey
	SystemKey
	SyncKey
	UnknownKey // FIXME this is aplaceholder
	unknownKey // allows us to iterate
)

var keyLabels = map[Key]string{
	AccessKey:     "access",
	AdminKey:      "admin",
	AuthKey:       "auth",
	BucketKey:     "bucket",
	CacheKey:      "cache",
	ChannelKey:    "channel",
	ChangesKey:    "changes",
	ClusterKey:    "cluster",
	ConfigKey:     "config",
	CRUDKey:       "crud",
	DCPKey:        "dcp",
	EventsKey:     "events",
	HTTPKey:       "http",
	ImportKey:     "import",
	JavascriptKey: "javascript",
	MigrateKey:    "migrate",
	QueryKey:      "query",
	ReplicateKey:  "replicate",
	SystemKey:     "system",
	SyncKey:       "sync",
}

type ConfigOpts struct {
	Redaction string                `json:"redaction_level"`
	Console   bool                  `json:"console,omitempty"` // changing requires a restart
	Level     zerolog.Level         `json:"log_level"`         // minimum level for all keys not set. defaults to None to support fine grained config
	Keys      map[Key]zerolog.Level `json:"keys,omitempty"`
	Local     bool                  `json:"localtime,omitempty"`
}

var config ConfigOpts

// export for ease TODO revisit names?

var loggers = make(map[Key]*zerolog.Logger)

// LevelError enables only error logging.
// LevelWarn enables warn, and error logging.
// LevelInfo enables info, warn, and error logging.
// LevelDebug enables debug, info, warn, and error logging.
// LevelTrace enables trace, debug, info, warn, and error logging logging.

func init() {
	zerolog.TimeFieldFormat = time.RFC3339

	log.Logger = log.Hook(zerolog.HookFunc(func(e *zerolog.Event, level zerolog.Level, message string) {
		// TODO add prometheus counts for Error and Warn level events
	}))

	for i := AuthKey; i < unknownKey; i++ {
		// TODO confirm the right level
		SetKeyLevel(i, zerolog.Disabled)
	}
}

// reassign the logger to set the level
func SetKeyLevel(k Key, v zerolog.Level) {
	l := log.Level(v).With().Str("key", keyLabels[k]).Logger()
	loggers[k] = &l
}

func SetGlobalLevel(v zerolog.Level) {
	zerolog.SetGlobalLevel(v)
}

func Initialize(conf ConfigOpts) {
	if conf.Console {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}
	SetGlobalLevel(conf.Level)
	for k, v := range conf.Keys {
		SetKeyLevel(k, v)
	}
}

// Logger wraps zerolog.Logger to add a more conventional interface
type Logger struct {
	log *zerolog.Logger
}

func (p Logger) Tracef(format string, vals ...any) {
	p.log.Trace().Msgf(format, vals)
}

func (p Logger) Debugf(format string, vals ...any) {
	p.log.Debug().Msgf(format, vals)
}

func (p Logger) Infof(format string, vals ...any) {
	p.log.Info().Msgf(format, vals)
}

func (p Logger) Warnf(format string, vals ...any) {
	p.log.Warn().Msgf(format, vals)
}

func (p Logger) Errorf(format string, vals ...any) {
	p.log.Error().Msgf(format, vals)
}

func (p Logger) Fatalf(format string, vals ...any) {
	p.log.Fatal().Msgf(format, vals)
}

func (p Logger) Panicf(format string, vals ...any) {
	p.log.Panic().Msgf(format, vals)
}

func For(k Key) *zerolog.Logger {
	return loggers[k]
}
