//go:build !windows
// +build !windows

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
	"syscall"

	"github.com/couchbase/sync_gateway/logger"
)

// Set Max File Descriptor limits
//
// Background information:
//
// - SG docs
//   http://developer.couchbase.com/documentation/mobile/1.1.0/develop/guides/sync-gateway/os-level-tuning/max-file-descriptors/index.html
// - Related SG issues
//   https://github.com/couchbase/sync_gateway/issues/1083
// - Hard limit vs Soft limit
//   http://unix.stackexchange.com/questions/29577/ulimit-difference-between-hard-and-soft-limits
func SetMaxFileDescriptors(requestedSoftFDLimit uint64) (uint64, error) {

	var limits syscall.Rlimit

	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limits); err != nil {
		return 0, err
	}

	requiresUpdate, recommendedSoftFDLimit := getSoftFDLimit(
		requestedSoftFDLimit,
		limits,
	)

	// No call to Setrlimit required, because the requested soft limit is lower than current soft limit
	if !requiresUpdate {
		return 0, nil
	}

	// Update the soft limit (but don't bother updating the hard limit, since only root can do that,
	// and it's assumed that this process is not running as root)
	limits.Cur = recommendedSoftFDLimit
	err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limits)

	if err == nil {
		logger.For(logger.SystemKey).Info().Msgf("Configured process to allow %d open file descriptors", recommendedSoftFDLimit)
	}

	return recommendedSoftFDLimit, err

}

// Get the recommended file descriptor settings
//
// Given:
//
// 1. The max file descriptors requested in the config (or default value)
// 2. The current ulimit settings
//
// return the recommended soft limit for the number of open file descriptors
// this process can have open.
//
// Rules:
//
// 1. Only return a value that is HIGHER than the existing soft limit, since
//    it is assumed to be user error to pass a config value that imposes a
//    a lower limit than the system limit
// 2. Only return a value that is LESS-THAN-OR-EQUAL to the existing hard limit
//    since trying to set something higher than the hard limit will fail
func getSoftFDLimit(requestedSoftFDLimit uint64, limit syscall.Rlimit) (requiresUpdate bool, recommendedSoftFDLimit uint64) {

	currentSoftFdLimit := limit.Cur
	currentHardFdLimit := limit.Max

	// Is the user requesting something that is less than the existing soft limit?
	if requestedSoftFDLimit <= currentSoftFdLimit {
		// yep, and there is no point in doing so, so return false for requiresUpdate.
		logger.For(logger.SystemKey).Debug().Msgf("requestedSoftFDLimit < currentSoftFdLimit (%v <= %v) no action needed", requestedSoftFDLimit, currentSoftFdLimit)
		return false, currentSoftFdLimit
	}

	// Is the user requesting something higher than the existing hard limit?
	if requestedSoftFDLimit >= currentHardFdLimit {
		// yes, so just use the hard limit
		logger.For(logger.SystemKey).Warn().Msgf("requestedSoftFDLimit >= currentHardFdLimit (%v >= %v) capping at %v", requestedSoftFDLimit, currentHardFdLimit, currentHardFdLimit)
		return true, currentHardFdLimit
	}

	// The user is requesting something higher than the existing soft limit
	// but lower than the existing hard limit, so allow this (and it will become
	// the new soft limit once the Setrlimit call has been made)
	return true, requestedSoftFDLimit

}
