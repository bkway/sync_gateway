package config

import (
	"github.com/couchbase/sync_gateway/logger"
	"github.com/knadh/koanf"
)

var conf = koanf.New(".")

type Config struct {
	Logging logger.ConfigOpts `json:"logging"`
}
