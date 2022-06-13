package config

import (
	"github.com/couchbase/sync_gateway/logger"
)

type Config struct {
	Logging logger.ConfigOpts `json:"logging"`
}
