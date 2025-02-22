/*
Copyright 2016-Present Couchbase, Inc.

Use of this software is governed by the Business Source License included in
the file licenses/BSL-Couchbase.txt.  As of the Change Date specified in that
file, in accordance with the Business Source License, use of this software will
be governed by the Apache License, Version 2.0, included in the file
licenses/APL2.txt.
*/

package base

import (
	"errors"
	"strings"
	"time"
)

const (

	// The username of the special "GUEST" user
	GuestUsername = "GUEST"

	// These settings are used when running unit tests against a live Couchbase Server to create/flush buckets
	DefaultCouchbaseAdministrator = "Administrator"
	DefaultCouchbasePassword      = "password"

	DefaultAutoImport = true // Whether Sync Gateway should auto-import docs, if not specified in the config

	DefaultUseXattrs      = true // Whether Sync Gateway uses xattrs for metadata storage, if not specified in the config
	DefaultAllowConflicts = true // Whether Sync Gateway allows revision conflicts, if not specified in the config

	DefaultDropIndexes = false // Whether Sync Gateway drops GSI indexes before each test while running in integration mode

	DefaultOldRevExpirySeconds = uint32(300)

	// Default value of _local document expiry
	DefaultLocalDocExpirySecs = uint32(60 * 60 * 24 * 90) //90 days in seconds

	DefaultViewQueryPageSize = 5000 // This must be greater than 1, or the code won't work due to windowing method

	// Until the sporadic integration tests failures in SG #3570 are fixed, should be GTE n1ql query timeout
	// to make it easier to identify root cause of test failures.
	DefaultWaitForSequence = time.Second * 30

	// Default the max number of idle connections per host to a relatively high number to avoid
	// excessive socket churn caused by opening short-lived connections and closing them after, which can cause
	// a high number of connections to end up in the TIME_WAIT state and exhaust system resources.  Since
	// GoCB is only connecting to a fixed set of Couchbase nodes, this number can be set relatively high and
	// still stay within a reasonable value.
	DefaultHttpMaxIdleConnsPerHost = 256

	// This primarily depends on MaxIdleConnsPerHost as the limiting factor, but sets some upper limit just to avoid
	// being completely unlimited
	DefaultHttpMaxIdleConns = "64000"

	// Keep idle connections around for a maximimum of 90 seconds.  This is the same value used by the Go DefaultTransport.
	DefaultHttpIdleConnTimeoutMilliseconds = "90000"

	// Number of kv connections (pipelines) per Couchbase Server node
	DefaultGocbKvPoolSize = "2"

	// The limit in Couchbase Server for total system xattr size
	couchbaseMaxSystemXattrSize = 1 * 1024 * 1024 // 1MB

	//==== Sync Prefix Documents & Keys ====
	SyncPrefix = "_sync:"

	AttPrefix              = SyncPrefix + "att:"
	Att2Prefix             = SyncPrefix + "att2:"
	BackfillCompletePrefix = SyncPrefix + "backfill:complete:"
	BackfillPendingPrefix  = SyncPrefix + "backfill:pending:"
	DCPCheckpointPrefix    = SyncPrefix + "dcp_ck:"
	RepairBackup           = SyncPrefix + "repair:backup:"
	RepairDryRun           = SyncPrefix + "repair:dryrun:"
	RevBodyPrefix          = SyncPrefix + "rb:"
	RevPrefix              = SyncPrefix + "rev:"
	RolePrefix             = SyncPrefix + "role:"
	SessionPrefix          = SyncPrefix + "session:"
	SGCfgPrefix            = SyncPrefix + "cfg"
	SyncSeqPrefix          = SyncPrefix + "seq:"
	UserEmailPrefix        = SyncPrefix + "useremail:"
	UserPrefix             = SyncPrefix + "user:"
	UnusedSeqPrefix        = SyncPrefix + "unusedSeq:"
	UnusedSeqRangePrefix   = SyncPrefix + "unusedSeqs:"

	DCPBackfillSeqKey = SyncPrefix + "dcp_backfill"
	SyncDataKey       = SyncPrefix + "syncdata"
	SyncSeqKey        = SyncPrefix + "seq"

	PersistentConfigPrefix = SyncPrefix + "dbconfig:"

	AttachmentCompactionXattrName = SyncXattrName + "-compact"

	SyncPropertyName = "_sync"
	SyncXattrName    = "_sync"

	// Intended to be used in Meta Map and related tests
	MetaMapXattrsKey = "xattrs"

	SGRStatusPrefix = SyncPrefix + "sgrStatus:"

	// Prefix for transaction metadata documents
	TxnPrefix = "_txn:"

	// Replication filter constants
	ByChannelFilter = "sync_gateway/bychannel"

	// Increase default gocbv2 op timeout to match the standard SG backoff retry timing used for gocb v1
	DefaultGocbV2OperationTimeout = 10 * time.Second

	// RedactedStr can be substituted in place of any sensitive data being returned by an API. The 'xxxxx' pattern is the same used by Go's url.Redacted() method.
	RedactedStr = "xxxxx"
)

const (
	SyncFnErrorMissingRole          = "sg missing role"
	SyncFnErrorAdminRequired        = "sg admin required"
	SyncFnErrorWrongUser            = "sg wrong user"
	SyncFnErrorMissingChannelAccess = "sg missing channel access"
)

const (
	// EmptyDocument denotes an empty document in JSON form.
	EmptyDocument = `{}`
)

var (
	SyncFnAccessErrors = []string{
		HTTPErrorf(403, SyncFnErrorMissingRole).Error(),
		HTTPErrorf(403, SyncFnErrorAdminRequired).Error(),
		HTTPErrorf(403, SyncFnErrorWrongUser).Error(),
		HTTPErrorf(403, SyncFnErrorMissingChannelAccess).Error(),
	}

	// Default warning thresholds
	DefaultWarnThresholdXattrSize       = 0.9 * float64(couchbaseMaxSystemXattrSize)
	DefaultWarnThresholdChannelsPerDoc  = uint32(50)
	DefaultWarnThresholdChannelsPerUser = uint32(50000)
	DefaultWarnThresholdGrantsPerDoc    = uint32(50)
	DefaultWarnThresholdChannelNameSize = uint32(250)
	DefaultClientPartitionWindow        = time.Hour * 24 * 30

	// ErrUnknownField is marked as the cause of the error when trying to decode a JSON snippet with unknown fields
	ErrUnknownField = errors.New("unrecognized JSON field")
)

func DCPCheckpointPrefixWithGroupID(groupID string) string {
	if groupID != "" {
		return DCPCheckpointPrefix + groupID + ":"
	}
	return DCPCheckpointPrefix
}

func SGCfgPrefixWithGroupID(groupID string) string {
	if groupID != "" {
		return SGCfgPrefix + ":" + groupID + ":"
	}
	return SGCfgPrefix
}

func SyncDataKeyWithGroupID(groupID string) string {
	if groupID != "" {
		return SyncDataKey + ":" + groupID
	}
	return SyncDataKey
}

// ServerIsTLS returns true if the server URL is using an accepted secure protocol as it's prefix
// Prefix checked: couchbases:
func ServerIsTLS(server string) bool {
	return strings.HasPrefix(server, "couchbases:")
}

// ServerIsWalrus returns true when the given server looks like a Walrus URI
// Equivalent to the old regexp: `^(walrus:|file:|/|\.)`
func ServerIsWalrus(server string) bool {
	return strings.HasPrefix(server, "walrus:") ||
		strings.HasPrefix(server, "file:") ||
		strings.HasPrefix(server, "/") ||
		strings.HasPrefix(server, ".")
}

const (
	MaxConcurrentSingleOps = 1000 // Max 1000 concurrent single bucket ops
	MaxConcurrentQueryOps  = 1000 // Max concurrent query ops

	// CRC-32 checksum represents the body hash of "Deleted" document.
	DeleteCrc32c = "0x00000000"
)
