/*
Copyright 2021-Present Couchbase, Inc.

Use of this software is governed by the Business Source License included in
the file licenses/BSL-Couchbase.txt.  As of the Change Date specified in that
file, in accordance with the Business Source License, use of this software will
be governed by the Apache License, Version 2.0, included in the file
licenses/APL2.txt.
*/

package base

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/couchbase/gocb/v2"
	sgbucket "github.com/couchbase/sg-bucket"
	"github.com/couchbase/sync_gateway/logger"
	pkgerrors "github.com/pkg/errors"
)

// View-related functionality for collections.  View operations are currently only supported
// by Couchbase Server at the bucket or default collection level, so all view operations here
// target the parent bucket for the collection.

// Metadata is returned as rawBytes when using ViewResultRaw.  viewMetadata used to retrieve
// TotalRows
type viewMetadata struct {
	TotalRows int `json:"total_rows,omitempty"`
}

// putDDocForTombstones uses the provided client and endpoints to create a design doc with index_xattr_on_deleted_docs=true
func putDDocForTombstones(name string, payload []byte, capiEps []string, client *http.Client, username string, password string) error {

	// From gocb.Bucket.getViewEp() - pick view endpoint at random
	if len(capiEps) == 0 {
		return errors.New("No available view nodes.")
	}
	viewEp := capiEps[rand.Intn(len(capiEps))]

	// Based on implementation in gocb.BucketManager.UpsertDesignDocument
	uri := fmt.Sprintf("/_design/%s", name)
	body := bytes.NewReader(payload)

	// Build the HTTP request
	req, err := http.NewRequest("PUT", viewEp+uri, body)
	if err != nil {
		return err
	}
	req.Header.Add("Content-Type", "application/json")
	req.SetBasicAuth(username, password)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer ensureBodyClosed(resp.Body)
	if resp.StatusCode != 201 {
		data, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf("Client error: %s", string(data))
	}

	return nil

}

// If the error is a net/url.Error and the error message is:
// 		net/http: request canceled while waiting for connection
// Then it means that the view request timed out, most likely due to the fact that it's a stale=false query and
// it's rebuilding the index.  In that case, it's desirable to return a more informative error than the
// underlying net/url.Error. See https://github.com/couchbase/sync_gateway/issues/2639
func isGoCBQueryTimeoutError(err error) bool {

	if err == nil {
		return false
	}

	// If it's not a *url.Error, then it's not a viewtimeout error
	netUrlError, ok := pkgerrors.Cause(err).(*url.Error)
	if !ok {
		return false
	}

	// If it's a *url.Error and contains the "request canceled" substring, then it's a viewtimeout error.
	return strings.Contains(netUrlError.Error(), "request canceled")

}

func (c *Collection) GetDDoc(docname string) (ddoc sgbucket.DesignDoc, err error) {
	manager := c.Bucket().ViewIndexes()
	designDoc, err := manager.GetDesignDocument(docname, gocb.DesignDocumentNamespaceProduction, nil)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return ddoc, ErrNotFound
		}
		return ddoc, err
	}

	// Serialize/deserialize to convert to sgbucket.DesignDoc
	designDocBytes, marshalErr := json.Marshal(designDoc)
	if marshalErr != nil {
		return ddoc, marshalErr
	}
	err = json.Unmarshal(designDocBytes, &ddoc)
	return ddoc, err
}

func (c *Collection) GetDDocs() (ddocs map[string]sgbucket.DesignDoc, err error) {
	manager := c.Bucket().ViewIndexes()
	gocbDDocs, getErr := manager.GetAllDesignDocuments(gocb.DesignDocumentNamespaceProduction, nil)
	if getErr != nil {
		return nil, getErr
	}

	result := make(map[string]gocb.DesignDocument, len(gocbDDocs))
	for _, ddoc := range gocbDDocs {
		result[ddoc.Name] = ddoc
	}

	// Serialize/deserialize to convert to sgbucket.DesignDoc
	resultBytes, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return nil, marshalErr
	}
	err = json.Unmarshal(resultBytes, &ddocs)
	return ddocs, err
}

func (c *Collection) PutDDoc(docname string, sgDesignDoc *sgbucket.DesignDoc) error {
	manager := c.Bucket().ViewIndexes()
	gocbDesignDoc := gocb.DesignDocument{
		Name:  docname,
		Views: make(map[string]gocb.View),
	}

	for viewName, view := range sgDesignDoc.Views {
		gocbView := gocb.View{
			Map:    view.Map,
			Reduce: view.Reduce,
		}
		gocbDesignDoc.Views[viewName] = gocbView
	}

	// If design doc needs to be tombstone-aware, requires custom creation*
	if sgDesignDoc.Options != nil && sgDesignDoc.Options.IndexXattrOnTombstones {
		return c.putDDocForTombstones(&gocbDesignDoc)
	}

	return manager.UpsertDesignDocument(gocbDesignDoc, gocb.DesignDocumentNamespaceProduction, nil)
}

// gocb doesn't have built-in support for the internal index_xattr_on_deleted_docs
// design doc property. XattrEnabledDesignDocV2 extends gocb.DesignDocument to support
// use of putDDocForTombstones
type XattrEnabledDesignDocV2 struct {
	*jsonDesignDocument
	IndexXattrOnTombstones bool `json:"index_xattr_on_deleted_docs,omitempty"`
}

// gocb's DesignDocument and View aren't directly marshallable for use in viewEp requests - they
// copy into *private* structs with the correct json annotations.  Cloning those here to support
// use of index_xattr_on_deleted_docs.
type jsonView struct {
	Map    string `json:"map,omitempty"`
	Reduce string `json:"reduce,omitempty"`
}

type jsonDesignDocument struct {
	Views map[string]jsonView `json:"views,omitempty"`
}

func asJsonDesignDocument(ddoc *gocb.DesignDocument) *jsonDesignDocument {
	jsonDDoc := &jsonDesignDocument{}
	jsonDDoc.Views = make(map[string]jsonView, 0)
	for name, view := range ddoc.Views {
		jsonDDoc.Views[name] = jsonView{
			Map:    view.Map,
			Reduce: view.Reduce,
		}
	}
	return jsonDDoc
}

type NoNameView struct {
	Map    string `json:"map,omitempty"`
	Reduce string `json:"reduce,omitempty"`
}

type NoNameDesignDocument struct {
	Name  string                `json:"-"`
	Views map[string]NoNameView `json:"views"`
}

func (c *Collection) putDDocForTombstones(ddoc *gocb.DesignDocument) error {
	username, password, _ := c.Spec.Auth.GetCredentials()
	agent, err := c.Bucket().Internal().IORouter()
	if err != nil {
		return fmt.Errorf("Unable to get handle for bucket router: %v", err)
	}

	jsonDdoc := asJsonDesignDocument(ddoc)

	xattrEnabledDesignDoc := XattrEnabledDesignDocV2{
		jsonDesignDocument:     jsonDdoc,
		IndexXattrOnTombstones: true,
	}
	data, err := json.Marshal(&xattrEnabledDesignDoc)
	if err != nil {
		return err
	}

	return putDDocForTombstones(ddoc.Name, data, agent.CapiEps(), agent.HTTPClient(), username, password)

}

func (c *Collection) DeleteDDoc(docname string) error {
	return c.Bucket().ViewIndexes().DropDesignDocument(docname, gocb.DesignDocumentNamespaceProduction, nil)
}

func (c *Collection) View(ddoc, name string, params map[string]interface{}) (sgbucket.ViewResult, error) {

	var viewResult sgbucket.ViewResult
	gocbViewResult, err := c.executeViewQuery(ddoc, name, params)
	if err != nil {
		return viewResult, err
	}

	if gocbViewResult != nil {
		viewResultIterator := &gocbRawIterator{
			rawResult:                  gocbViewResult,
			concurrentQueryOpLimitChan: c.queryOps,
		}
		for {
			viewRow := sgbucket.ViewRow{}
			if gotRow := viewResultIterator.Next(&viewRow); gotRow == false {
				break
			}
			viewResult.Rows = append(viewResult.Rows, &viewRow)
		}

		// Check for errors
		err = gocbViewResult.Err()
		if err != nil {
			viewErr := sgbucket.ViewError{
				Reason: err.Error(),
			}
			viewResult.Errors = append(viewResult.Errors, viewErr)
		}

		viewMeta, err := unmarshalViewMetadata(gocbViewResult)
		if err != nil {
			logger.For(logger.QueryKey).Err(err).Msgf("Unable to type get metadata for gocb ViewResult - the total rows count will be missing.")
		} else {
			viewResult.TotalRows = viewMeta.TotalRows
		}
		_ = viewResultIterator.Close()

	}

	// Indicate the view response contained partial errors so consumers can determine
	// if the result is valid to their particular use-case (see SG issue #2383)
	if len(viewResult.Errors) > 0 {
		return viewResult, ErrPartialViewErrors
	}

	return viewResult, nil
}

func unmarshalViewMetadata(viewResult *gocb.ViewResultRaw) (viewMetadata, error) {
	var viewMeta viewMetadata
	rawMeta, err := viewResult.MetaData()
	if err == nil {
		err = json.Unmarshal(rawMeta, &viewMeta)
	}
	return viewMeta, err
}

func (c *Collection) ViewQuery(ddoc, name string, params map[string]interface{}) (sgbucket.QueryResultIterator, error) {

	gocbViewResult, err := c.executeViewQuery(ddoc, name, params)
	if err != nil {
		return nil, err
	}
	return &gocbRawIterator{rawResult: gocbViewResult, concurrentQueryOpLimitChan: c.queryOps}, nil
}

func (c *Collection) executeViewQuery(ddoc, name string, params map[string]interface{}) (*gocb.ViewResultRaw, error) {
	viewResult := sgbucket.ViewResult{}
	viewResult.Rows = sgbucket.ViewRows{}

	viewOpts, optsErr := createViewOptions(params)
	if optsErr != nil {
		return nil, optsErr
	}

	c.waitForAvailQueryOp()
	goCbViewResult, err := c.Bucket().ViewQuery(ddoc, name, viewOpts)

	// On timeout, return an typed error
	if isGoCBQueryTimeoutError(err) {
		c.releaseQueryOp()
		return nil, ErrViewTimeoutError
	} else if err != nil {
		c.releaseQueryOp()
		return nil, pkgerrors.WithStack(err)
	}

	return goCbViewResult.Raw(), nil
}

// waitForAvailQueryOp prevents Sync Gateway from having too many concurrent
// queries against Couchbase Server
func (c *Collection) waitForAvailQueryOp() {
	c.queryOps <- struct{}{}
}

func (c *Collection) releaseQueryOp() {
	<-c.queryOps
}

func asBool(value interface{}) bool {

	switch typeValue := value.(type) {
	case string:
		parsedVal, err := strconv.ParseBool(typeValue)
		if err != nil {
			logger.For(logger.BucketKey).Warn().Err(err).Msgf("asBool called with unknown value: %v.  defaulting to false", typeValue)
			return false
		}
		return parsedVal
	case bool:
		return typeValue
	default:
		logger.For(logger.BucketKey).Warn().Msgf("asBool called with unknown type: %T.  defaulting to false", typeValue)
		return false
	}

}

func normalizeIntToUint(value interface{}) (uint, error) {
	switch typeValue := value.(type) {
	case int:
		return uint(typeValue), nil
	case uint64:
		return uint(typeValue), nil
	case string:
		i, err := strconv.Atoi(typeValue)
		return uint(i), err
	default:
		return uint(0), fmt.Errorf("Unable to convert %v (%T) -> uint.", value, value)
	}
}

// Applies the viewquery options as specified in the params map to the gocb.ViewOptions
func createViewOptions(params map[string]interface{}) (viewOpts *gocb.ViewOptions, err error) {

	viewOpts = &gocb.ViewOptions{}
	for optionName, optionValue := range params {
		switch optionName {
		case ViewQueryParamStale:
			viewOpts.ScanConsistency = asViewConsistency(optionValue)
		case ViewQueryParamReduce:
			viewOpts.Reduce = asBool(optionValue)
		case ViewQueryParamLimit:
			uintVal, err := normalizeIntToUint(optionValue)
			if err != nil {
				logger.For(logger.QueryKey).Warn().Err(err).Msg("ViewQueryParamLimit error")
			}
			viewOpts.Limit = uint32(uintVal)
		case ViewQueryParamDescending:
			if asBool(optionValue) == true {
				viewOpts.Order = gocb.ViewOrderingDescending
			}
		case ViewQueryParamSkip:
			uintVal, err := normalizeIntToUint(optionValue)
			if err != nil {
				logger.For(logger.QueryKey).Warn().Err(err).Msg("ViewQueryParamSkip error")
			}
			viewOpts.Skip = uint32(uintVal)
		case ViewQueryParamGroup:
			viewOpts.Group = asBool(optionValue)
		case ViewQueryParamGroupLevel:
			uintVal, err := normalizeIntToUint(optionValue)
			if err != nil {
				logger.For(logger.QueryKey).Warn().Err(err).Msg("ViewQueryParamGroupLevel error")
			}
			viewOpts.GroupLevel = uint32(uintVal)
		case ViewQueryParamKey:
			viewOpts.Key = optionValue
		case ViewQueryParamKeys:
			keys, err := ConvertToEmptyInterfaceSlice(optionValue)
			if err != nil {
				return nil, err
			}
			viewOpts.Keys = keys
		case ViewQueryParamStartKey, ViewQueryParamEndKey, ViewQueryParamInclusiveEnd, ViewQueryParamStartKeyDocId, ViewQueryParamEndKeyDocId:
			// These are dealt with outside of this case statement to build ranges
		case ViewQueryParamIncludeDocs:
			// Ignored -- see https://forums.couchbase.com/t/do-the-viewquery-options-omit-include-docs-on-purpose/12399
		default:
			return nil, fmt.Errorf("Unexpected view query param: %v.  This will be ignored", optionName)
		}
	}

	// Range: startkey, endkey, inclusiveend
	var startKey, endKey interface{}
	if _, ok := params[ViewQueryParamStartKey]; ok {
		startKey = params[ViewQueryParamStartKey]
	}
	if _, ok := params[ViewQueryParamEndKey]; ok {
		endKey = params[ViewQueryParamEndKey]
	}

	// Default value of inclusiveEnd in Couchbase Server is true (if not specified)
	inclusiveEnd := true
	if _, ok := params[ViewQueryParamInclusiveEnd]; ok {
		inclusiveEnd = asBool(params[ViewQueryParamInclusiveEnd])
	}
	viewOpts.StartKey = startKey
	viewOpts.EndKey = endKey
	viewOpts.InclusiveEnd = inclusiveEnd

	// IdRange: startKeyDocId, endKeyDocId
	startKeyDocId := ""
	endKeyDocId := ""
	if _, ok := params[ViewQueryParamStartKeyDocId]; ok {
		startKeyDocId = params[ViewQueryParamStartKeyDocId].(string)
	}
	if _, ok := params[ViewQueryParamEndKeyDocId]; ok {
		endKeyDocId = params[ViewQueryParamEndKeyDocId].(string)
	}
	viewOpts.StartKeyDocID = startKeyDocId
	viewOpts.EndKeyDocID = endKeyDocId
	return viewOpts, nil
}

// Used to convert the stale view parameter to a gocb ViewScanConsistency
func asViewConsistency(value interface{}) gocb.ViewScanConsistency {

	switch typeValue := value.(type) {
	case string:
		if typeValue == "ok" {
			return gocb.ViewScanConsistencyNotBounded
		}
		if typeValue == "update_after" {
			return gocb.ViewScanConsistencyUpdateAfter
		}
		parsedVal, err := strconv.ParseBool(typeValue)
		if err != nil {
			logger.For(logger.QueryKey).Warn().Err(err).Msgf("asStale called with unknown value: %v.  defaulting to stale=false", typeValue)
			return gocb.ViewScanConsistencyRequestPlus
		}
		if parsedVal {
			return gocb.ViewScanConsistencyNotBounded
		} else {
			return gocb.ViewScanConsistencyRequestPlus
		}
	case bool:
		if typeValue {
			return gocb.ViewScanConsistencyNotBounded
		} else {
			return gocb.ViewScanConsistencyRequestPlus
		}
	default:
		logger.For(logger.QueryKey).Warn().Msgf("asViewConsistency called with unknown type: %T.  defaulting to RequestPlus", typeValue)
		return gocb.ViewScanConsistencyRequestPlus
	}

}
