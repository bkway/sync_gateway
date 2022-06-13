//  Copyright 2013-Present Couchbase, Inc.
//
//  Use of this software is governed by the Business Source License included
//  in the file licenses/BSL-Couchbase.txt.  As of the Change Date specified
//  in that file, in accordance with the Business Source License, use of this
//  software will be governed by the Apache License, Version 2.0, included in
//  the file licenses/APL2.txt.

package channels

import (
	"strings"

	"github.com/couchbase/sync_gateway/base"
	"github.com/couchbase/sync_gateway/utils"
)

type StarMode int

const (
	RemoveStar = StarMode(iota)
	KeepStar
	ExpandStar
)

// Constants for the * channel variations
const UserStarChannel = "*"     // user channel for "can access all docs"
const DocumentStarChannel = "!" // doc channel for "visible to all users"
const AllChannelWildcard = "*"  // wildcard for 'all channels'

func illegalChannelError(name string) error {
	return base.HTTPErrorf(400, "Illegal channel name %q", name)
}

func IsValidChannel(channel string) bool {
	return len(channel) > 0 && !strings.Contains(channel, ",")
}

// Creates a new Set from an array of strings. Returns an error if any names are invalid.
func SetFromArray(names []string, mode StarMode) (utils.Set, error) {
	for _, name := range names {
		if !IsValidChannel(name) {
			return nil, illegalChannelError(name)
		}
	}
	result := utils.SetFromArray(names)
	switch mode {
	case RemoveStar:
		result = result.Removing(UserStarChannel)
	case ExpandStar:
		if result.Contains(UserStarChannel) {
			result = utils.SetOf(UserStarChannel)
		}
	}
	return result, nil
}

// If the set contains "*", returns a set of only "*". Else returns the original set.
func ExpandingStar(set utils.Set) utils.Set {
	if _, exists := set[UserStarChannel]; exists {
		return utils.SetOf(UserStarChannel)
	}
	return set
}

// Returns a set with any "*" channel removed.
func IgnoringStar(set utils.Set) utils.Set {
	return set.Removing(UserStarChannel)
}
