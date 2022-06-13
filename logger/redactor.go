/*
Copyright 2018-Present Couchbase, Inc.

Use of this software is governed by the Business Source License included in
the file licenses/BSL-Couchbase.txt.  As of the Change Date specified in that
file, in accordance with the Business Source License, use of this software will
be governed by the Apache License, Version 2.0, included in the file
licenses/APL2.txt.
*/

package logger

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/couchbase/sync_gateway/utils"
	pkgerrors "github.com/pkg/errors"
)

// Redactor provides an interface for log redaction.
type Redactor interface {
	// Redact returns the given string in a redacted form. This may be tagged,
	// changed, hashed, or removed completely depending on desired behaviour.
	Redact() string
	// String returns the non-redacted form of the given string.
	String() string
}

// This allows for lazy evaluation for a Redactor. Means that we don't have to process redaction unless we are
// definitely performing a redaction
type RedactorFunc func() Redactor

func (p RedactorFunc) String() string {
	return p().String()
}

func (p RedactorFunc) Redact() string {
	return p().Redact()
}

type RedactorSlice []Redactor

func (p RedactorSlice) Redact() string {
	r := make([]string, len(p))
	for i, v := range p {
		r[i] = v.Redact()
	}
	return fmt.Sprintf("[ %s ]", strings.Join(r, ", "))
}

func (p RedactorSlice) String() string {
	r := make([]string, len(p))
	for i, v := range p {
		r[i] = v.String()
	}
	return fmt.Sprintf("[ %s ]", strings.Join(r, ", "))
}

// redact performs an *in-place* redaction on the input slice, and returns it.
// This should only be consumed by logging funcs. E.g. fmt.Printf(fmt, redact(args))
func redact(args []interface{}) []interface{} {
	for i, v := range args {
		if r, ok := v.(Redactor); ok {
			args[i] = r.Redact()
		} else if err, ok := v.(error); ok {
			// it's an error, and may need to be unwrapped before it can be redacted
			err = pkgerrors.Cause(err)
			if r, ok := err.(Redactor); ok {
				args[i] = r.Redact()
			}
		}
	}
	return args
}

func buildRedactorSet(set utils.Set, function func(interface{}) RedactorFunc) RedactorSet {
	return RedactorSet{
		set:      set,
		redactor: function,
	}
}

type RedactorSet struct {
	set      utils.Set
	redactor func(interface{}) RedactorFunc
}

func (p RedactorSet) Redact() string {
	tmp := make([]string, len(p.set))
	for i, v := range p.set.ToArray() {
		tmp[i] = p.redactor(v).Redact()
	}
	return fmt.Sprintf("{ %s }", strings.Join(tmp, ", "))
}

func (p RedactorSet) String() string {
	tmp := make([]string, len(p.set))
	for i, v := range p.set.ToArray() {
		tmp[i] = p.redactor(v).String()
	}
	return fmt.Sprintf("{ %s }", strings.Join(tmp, ", "))
}

func buildRedactorFuncSlice(valueOf reflect.Value, function func(interface{}) RedactorFunc) RedactorSlice {
	length := valueOf.Len()
	retVal := make([]Redactor, 0, length)
	for i := 0; i < length; i++ {
		retVal = append(retVal, function(valueOf.Index(i).Interface()))
	}

	return retVal
}

const DefaultRedactionLevel = RedactPartial

const (
	RedactUnset RedactionLevel = iota
	RedactNone
	RedactPartial
	RedactFull
)

func SetRedaction(redactionLevel RedactionLevel) {
	switch redactionLevel {
	case RedactFull:
		RedactUserData = true
	case RedactPartial:
		RedactUserData = true
	case RedactNone:
		RedactUserData = false
	default:
		RedactUserData = true
	}
}

type RedactionLevel int

// String returns a lower-case ASCII representation of the log redaction level.
func (l RedactionLevel) String() string {
	switch l {
	case RedactNone:
		return "none"
	case RedactPartial:
		return "partial"
	case RedactFull:
		return "full"
	case RedactUnset:
		return "unset"
	default:
		return fmt.Sprintf("RedactionLevel(%d)", l)
	}
}

// MarshalText marshals the RedactionLevel to text.
func (l RedactionLevel) MarshalText() ([]byte, error) {
	return []byte(l.String()), nil
}

// UnmarshalText unmarshals text to a RedactionLevel.
func (l *RedactionLevel) UnmarshalText(text []byte) error {
	switch strings.ToLower(string(text)) {
	case "none":
		*l = RedactNone
	case "partial":
		*l = RedactPartial
	case "full":
		*l = RedactFull
	case "unset":
		*l = RedactUnset
	default:
		return fmt.Errorf("unrecognized redaction level: %q", text)
	}
	return nil
}
