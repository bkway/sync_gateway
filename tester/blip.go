package tester

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/couchbase/go-blip"
	"github.com/couchbase/sync_gateway/base"
	"github.com/couchbase/sync_gateway/db"
	"github.com/couchbase/sync_gateway/rest"
)

// The parameters used to create a BlipTester
type BlipTesterSpec struct {

	// Run Sync Gateway in "No conflicts" mode.  Will be propgated to the underyling RestTester
	noConflictsMode bool

	// If an underlying RestTester is created, it will propagate this setting to the underlying RestTester.
	guestEnabled bool

	// The Sync Gateway username and password to connect with.  If set, then you
	// may want to disable "Admin Party" mode, which will allow guest user access.
	// By default, the created user will have access to a single channel that matches their username.
	// If you need to grant the user access to more channels, you can override this behavior with the
	// connectingUserChannelGrants field
	connectingUsername string
	connectingPassword string

	// By default, the created user will have access to a single channel that matches their username.
	// If you need to grant the user access to more channels, you can override this behavior by specifying
	// the channels the user should have access in this string slice
	connectingUserChannelGrants []string

	// Allow tests to further customized a RestTester or re-use it across multiple BlipTesters if needed.
	// If a RestTester is passed in, certain properties of the BlipTester such as guestEnabled will be ignored, since
	// those properties only affect the creation of the RestTester.
	// If nil, a default restTester will be created based on the properties in this spec
	// restTester *RestTester

	// Supported blipProtocols for the client to use in order of preference
	blipProtocols []string
}

// State associated with a BlipTester
// Note that it's not safe to have multiple goroutines access a single BlipTester due to the
// fact that certain methods register profile handlers on the BlipContext
type BlipTester struct {

	// The underlying RestTester which is used to bootstrap the initial blip websocket creation,
	// as well as providing a way for tests to access Sync Gateway over REST to hit admin-only endpoints
	// which are not available via blip.  Since a test may need to create multiple BlipTesters for multiple
	// user contexts, a single RestTester may be shared among multiple BlipTester instances.
	restTester *RestTester

	// This flag is used to avoid closing the contained restTester. This functionality is to avoid a double close in
	// some areas.
	avoidRestTesterClose bool

	// The blip context which contains blip related state and the sender/reciever goroutines associated
	// with this websocket connection
	blipContext *blip.Context

	// The blip sender that can be used for sending messages over the websocket connection
	sender *blip.Sender
}

// Close the bliptester
func (bt BlipTester) Close() {
	bt.sender.Close()
	if !bt.avoidRestTesterClose {
		bt.restTester.Close()
	}
}

// Returns database context for blipTester (assumes underlying rest tester is based on a single db - returns first it finds)
func (bt BlipTester) DatabaseContext() *db.DatabaseContext {
	dbs := bt.restTester.ServerContext().AllDatabases()
	for _, database := range dbs {
		return database
	}
	return nil
}

func NewBlipTesterFromSpecWithRT(tb testing.TB, spec *BlipTesterSpec, rt *RestTester) (blipTester *BlipTester, err error) {
	blipTesterSpec := spec
	if spec == nil {
		// Default spec
		blipTesterSpec = &BlipTesterSpec{}
	}
	blipTester, err = createBlipTesterWithSpec(tb, *blipTesterSpec, rt)
	if err != nil {
		return nil, err
	}
	blipTester.avoidRestTesterClose = true

	return blipTester, err
}

// Create a BlipTester using the default spec
func NewBlipTester(tb testing.TB) (*BlipTester, error) {
	defaultSpec := BlipTesterSpec{guestEnabled: true}
	return NewBlipTesterFromSpec(tb, defaultSpec)
}

func NewBlipTesterFromSpec(tb testing.TB, spec BlipTesterSpec) (*BlipTester, error) {
	rtConfig := RestTesterConfig{
		EnableNoConflictsMode: spec.noConflictsMode,
		guestEnabled:          spec.guestEnabled,
	}
	var rt = NewRestTester(tb, &rtConfig)
	return createBlipTesterWithSpec(tb, spec, rt)
}

// Create a BlipTester using the given spec
func createBlipTesterWithSpec(tb testing.TB, spec BlipTesterSpec, rt *RestTester) (*BlipTester, error) {
	bt := &BlipTester{
		restTester: rt,
	}

	// Since blip requests all go over the public handler, wrap the public handler with the httptest server
	publicHandler := bt.restTester.TestPublicHandler()

	if len(spec.connectingUsername) > 0 {

		// By default, the user will be granted access to a single channel equal to their username
		adminChannels := []string{spec.connectingUsername}

		// If the caller specified a list of channels to grant the user access to, then use that instead.
		if len(spec.connectingUserChannelGrants) > 0 {
			adminChannels = []string{} // empty it
			adminChannels = append(adminChannels, spec.connectingUserChannelGrants...)
		}

		// serialize admin channels to json array
		adminChannelsJson, err := json.Marshal(adminChannels)
		if err != nil {
			return nil, err
		}
		adminChannelsStr := fmt.Sprintf("%s", adminChannelsJson)

		userDocBody := fmt.Sprintf(`{"name":"%s", "password":"%s", "admin_channels":%s}`,
			spec.connectingUsername,
			spec.connectingPassword,
			adminChannelsStr,
		)
		log.Printf("Creating user: %v", userDocBody)

		// Create a user.  NOTE: this must come *after* the bt.rt.TestPublicHandler() call, otherwise it will end up getting ignored
		_ = bt.restTester.SendAdminRequest(
			"POST",
			"/db/_user/",
			userDocBody,
		)
	}

	// Create a _temporary_ test server bound to an actual port that is used to make the blip connection.
	// This is needed because the mock-based approach fails with a "Connection not hijackable" error when
	// trying to do the websocket upgrade.  Since it's only needed to setup the websocket, it can be closed
	// as soon as the websocket is established, hence the defer srv.Close() call.
	srv := httptest.NewServer(publicHandler)
	defer srv.Close()

	// Construct URL to connect to blipsync target endpoint
	destUrl := fmt.Sprintf("%s/db/_blipsync", srv.URL)
	u, err := url.Parse(destUrl)
	if err != nil {
		return nil, err
	}
	u.Scheme = "ws"

	// If protocols are not set use V3 as a V3 client would
	protocols := spec.blipProtocols
	if len(protocols) == 0 {
		protocols = []string{db.BlipCBMobileReplicationV3}
	}

	// Make BLIP/Websocket connection
	// FIXME ctx is wrong
	bt.blipContext, err = db.NewSGBlipContextWithProtocols(context.TODO(), "", protocols...)
	// bt.blipContext, err = db.NewSGBlipContextWithProtocols(logger.TestCtx(tb), "", protocols...)
	if err != nil {
		return nil, err
	}

	config := blip.DialOptions{
		URL: u.String(),
	}

	if len(spec.connectingUsername) > 0 {
		config.HTTPHeader = http.Header{
			"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(spec.connectingUsername+":"+spec.connectingPassword))},
		}
	}

	bt.sender, err = bt.blipContext.DialConfig(&config)
	if err != nil {
		return nil, err
	}

	return bt, nil

}

func (bt *BlipTester) SetCheckpoint(client string, checkpointRev string, body []byte) (sent bool, req *db.SetCheckpointMessage, res *db.SetCheckpointResponse, err error) {

	scm := db.NewSetCheckpointMessage()
	scm.SetCompressed(true)
	scm.SetClient(client)
	scm.SetRev(checkpointRev)
	scm.SetBody(body)

	sent = bt.sender.Send(scm.Message)
	if !sent {
		return sent, scm, nil, fmt.Errorf("Failed to send setCheckpoint for client: %v", client)
	}

	scr := &db.SetCheckpointResponse{Message: scm.Response()}
	return true, scm, scr, nil

}

// The docHistory should be in the same format as expected by db.PutExistingRevWithBody(), or empty if this is the first revision
func (bt *BlipTester) SendRevWithHistory(docId, docRev string, revHistory []string, body []byte, properties blip.Properties) (sent bool, req, res *blip.Message, err error) {

	revRequest := blip.NewRequest()
	revRequest.SetCompressed(true)
	revRequest.SetProfile("rev")

	revRequest.Properties["id"] = docId
	revRequest.Properties["rev"] = docRev
	revRequest.Properties["deleted"] = "false"
	if len(revHistory) > 0 {
		revRequest.Properties["history"] = strings.Join(revHistory, ",")
	}

	// Override any properties which have been supplied explicitly
	for k, v := range properties {
		revRequest.Properties[k] = v
	}

	revRequest.SetBody(body)
	sent = bt.sender.Send(revRequest)
	if !sent {
		return sent, revRequest, nil, fmt.Errorf("Failed to send revRequest for doc: %v", docId)
	}
	revResponse := revRequest.Response()
	if revResponse.SerialNumber() != revRequest.SerialNumber() {
		return sent, revRequest, revResponse, fmt.Errorf("revResponse.SerialNumber() != revRequest.SerialNumber().  %v != %v", revResponse.SerialNumber(), revRequest.SerialNumber())
	}

	// Make sure no errors.  Just panic for now, but if there are tests that expect errors and want
	// to use SendRev(), this could be returned.
	if errorCode, ok := revResponse.Properties["Error-Code"]; ok {
		body, _ := revResponse.Body()
		return sent, revRequest, revResponse, fmt.Errorf("Unexpected error sending rev: %v\n%s", errorCode, body)
	}

	return sent, revRequest, revResponse, nil

}

func (bt *BlipTester) SendRev(docId, docRev string, body []byte, properties blip.Properties) (sent bool, req, res *blip.Message, err error) {

	return bt.SendRevWithHistory(docId, docRev, []string{}, body, properties)

}

// Get a doc at a particular revision from Sync Gateway.
//
// Warning: this can only be called from a single goroutine, given the fact it registers profile handlers.
//
// If that is not found, it will return an empty resultDoc with no errors.
//
// - Call subChanges (continuous=false) endpoint to get all changes from Sync Gateway
// - Respond to each "change" request telling the other side to send the revision
//		- NOTE: this could be made more efficient by only requesting the revision for the docid/revid pair
//              passed in the parameter.
// - If the rev handler is called back with the desired docid/revid pair, save that into a variable that will be returned
// - Block until all pending operations are complete
// - Return the resultDoc or an empty resultDoc
//
func (bt *BlipTester) GetDocAtRev(requestedDocID, requestedDocRev string) (resultDoc rest.RestDocument, err error) {

	docs := map[string]rest.RestDocument{}
	changesFinishedWg := sync.WaitGroup{}
	revsFinishedWg := sync.WaitGroup{}

	defer func() {
		// Clean up all profile handlers that are registered as part of this test
		delete(bt.blipContext.HandlerForProfile, "changes")
		delete(bt.blipContext.HandlerForProfile, "rev")
	}()

	// -------- Changes handler callback --------
	bt.blipContext.HandlerForProfile["changes"] = func(request *blip.Message) {

		// Send a response telling the other side we want ALL revisions

		body, err := request.Body()
		if err != nil {
			panic(fmt.Sprintf("Error getting request body: %v", err))
		}

		if string(body) == "null" {
			changesFinishedWg.Done()
			return
		}

		if !request.NoReply() {

			// unmarshal into json array
			changesBatch := [][]interface{}{}

			if err := json.Unmarshal(body, &changesBatch); err != nil {
				panic(fmt.Sprintf("Error unmarshalling changes. Body: %vs.  Error: %v", string(body), err))
			}

			responseVal := [][]interface{}{}
			for _, change := range changesBatch {
				revId := change[2].(string)
				responseVal = append(responseVal, []interface{}{revId})
				revsFinishedWg.Add(1)
			}

			response := request.Response()
			responseValBytes, err := json.Marshal(responseVal)
			log.Printf("responseValBytes: %s", responseValBytes)
			if err != nil {
				panic(fmt.Sprintf("Error marshalling response: %v", err))
			}
			response.SetBody(responseValBytes)

		}
	}

	// -------- Rev handler callback --------
	bt.blipContext.HandlerForProfile["rev"] = func(request *blip.Message) {

		defer revsFinishedWg.Done()
		body, err := request.Body()
		var doc rest.RestDocument
		err = json.Unmarshal(body, &doc)
		if err != nil {
			panic(fmt.Sprintf("Unexpected err: %v", err))
		}
		docId := request.Properties["id"]
		docRev := request.Properties["rev"]
		doc.SetID(docId)
		doc.SetRevID(docRev)
		docs[docId] = doc

		if docId == requestedDocID && docRev == requestedDocRev {
			resultDoc = doc
		}

	}

	// Send subChanges to subscribe to changes, which will cause the "changes" profile handler above to be called back
	changesFinishedWg.Add(1)
	subChangesRequest := blip.NewRequest()
	subChangesRequest.SetProfile("subChanges")
	subChangesRequest.Properties["continuous"] = "false"

	sent := bt.sender.Send(subChangesRequest)
	if !sent {
		panic(fmt.Sprintf("Unable to subscribe to changes."))
	}

	changesFinishedWg.Wait()
	revsFinishedWg.Wait()

	return resultDoc, nil

}

// Warning: this can only be called from a single goroutine, given the fact it registers profile handlers.
func (bt *BlipTester) SendRevWithAttachment(input rest.SendRevWithAttachmentInput) (sent bool, req, res *blip.Message) {

	defer func() {
		// Clean up all profile handlers that are registered as part of this test
		delete(bt.blipContext.HandlerForProfile, "getAttachment")
	}()

	// Create a doc with an attachment
	myAttachment := db.DocAttachment{
		ContentType: "application/json",
		Digest:      input.attachmentDigest,
		Length:      input.attachmentLength,
		Revpos:      1,
		Stub:        true,
	}

	doc := rest.NewRestDocument()
	if len(input.body) > 0 {
		unmarshalErr := json.Unmarshal(input.body, &doc)
		if unmarshalErr != nil {
			panic(fmt.Sprintf("Error unmarshalling body into restDocument.  Error: %v", unmarshalErr))
		}
	}

	doc.SetAttachments(db.AttachmentMap{
		input.attachmentName: &myAttachment,
	})

	docBody, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("Error marshalling doc.  Error: %v", err))
	}

	getAttachmentWg := sync.WaitGroup{}

	bt.blipContext.HandlerForProfile["getAttachment"] = func(request *blip.Message) {
		defer getAttachmentWg.Done()
		if request.Properties["digest"] != myAttachment.Digest {
			panic(fmt.Sprintf("Unexpected digest.  Got: %v, expected: %v", request.Properties["digest"], myAttachment.Digest))
		}
		response := request.Response()
		response.SetBody([]byte(input.attachmentBody))
	}

	// Push a rev with an attachment.
	getAttachmentWg.Add(1)
	sent, req, res, _ = bt.SendRevWithHistory(
		input.docId,
		input.revId,
		input.history,
		docBody,
		blip.Properties{},
	)

	// Expect a callback to the getAttachment endpoint
	getAttachmentWg.Wait()

	return sent, req, res

}

func (bt *BlipTester) WaitForNumChanges(numChangesExpected int) (changes [][]interface{}) {

	retryWorker := func() (shouldRetry bool, err error, value interface{}) {
		currentChanges := bt.GetChanges()
		if len(currentChanges) >= numChangesExpected {
			return false, nil, currentChanges
		}

		// haven't seen numDocsExpected yet, so wait and retry
		return true, nil, nil

	}

	_, rawChanges := base.RetryLoop(
		"WaitForNumChanges",
		retryWorker,
		base.CreateDoublingSleeperFunc(10, 10),
	)

	changes, _ = rawChanges.([][]interface{})
	return changes

}

// Returns changes in form of [[sequence, docID, revID, deleted], [sequence, docID, revID, deleted]]
// Warning: this can only be called from a single goroutine, given the fact it registers profile handlers.
func (bt *BlipTester) GetChanges() (changes [][]interface{}) {

	defer func() {
		// Clean up all profile handlers that are registered as part of this test
		delete(bt.blipContext.HandlerForProfile, "changes") // a handler for this profile is registered in SubscribeToChanges
	}()

	collectedChanges := [][]interface{}{}
	chanChanges := make(chan *blip.Message)
	bt.SubscribeToChanges(false, chanChanges)

	for changeMsg := range chanChanges {

		body, err := changeMsg.Body()
		if err != nil {
			panic(fmt.Sprintf("Error getting request body: %v", err))
		}

		if string(body) == "null" {
			// the other side indicated that it's done sending changes.
			// this only works (I think) because continuous=false.
			close(chanChanges)
			break
		}

		// unmarshal into json array
		changesBatch := [][]interface{}{}

		if err := json.Unmarshal(body, &changesBatch); err != nil {
			panic(fmt.Sprintf("Error unmarshalling changes. Body: %vs.  Error: %v", string(body), err))
		}

		for _, change := range changesBatch {
			collectedChanges = append(collectedChanges, change)
		}

	}

	return collectedChanges

}

func (bt *BlipTester) WaitForNumDocsViaChanges(numDocsExpected int) (docs map[string]rest.RestDocument, ok bool) {

	retryWorker := func() (shouldRetry bool, err error, value interface{}) {
		fmt.Println("BT WaitForNumDocsViaChanges retry")
		allDocs := bt.PullDocs()
		if len(allDocs) >= numDocsExpected {
			return false, nil, allDocs
		}

		// haven't seen numDocsExpected yet, so wait and retry
		return true, nil, nil

	}

	_, allDocs := base.RetryLoop(
		"WaitForNumDocsViaChanges",
		retryWorker,
		base.CreateDoublingSleeperFunc(20, 10),
	)

	docs, ok = allDocs.(map[string]rest.RestDocument)
	return docs, ok
}

// Get all documents and their attachments via the following steps:
//
// - Invoking one-shot subChanges request
// - Responding to all incoming "changes" requests from peer to request the changed rev, and accumulate rev body
// - Responding to all incoming "rev" requests from peer to get all attachments, and accumulate them
// - Return accumulated docs + attachments to caller
//
// It is basically a pull replication without the checkpointing
// Warning: this can only be called from a single goroutine, given the fact it registers profile handlers.
func (bt *BlipTester) PullDocs() (docs map[string]rest.RestDocument) {

	docs = map[string]rest.RestDocument{}

	// Mutex to avoid write contention on docs while PullDocs is running (as rev messages may be processed concurrently)
	var docsLock sync.Mutex
	changesFinishedWg := sync.WaitGroup{}
	revsFinishedWg := sync.WaitGroup{}

	defer func() {
		// Clean up all profile handlers that are registered as part of this test
		delete(bt.blipContext.HandlerForProfile, "changes")
		delete(bt.blipContext.HandlerForProfile, "rev")
	}()

	// -------- Changes handler callback --------
	// When this test sends subChanges, Sync Gateway will send a changes request that must be handled
	bt.blipContext.HandlerForProfile["changes"] = func(request *blip.Message) {

		// Send a response telling the other side we want ALL revisions

		body, err := request.Body()
		if err != nil {
			panic(fmt.Sprintf("Error getting request body: %v", err))
		}

		if string(body) == "null" {
			changesFinishedWg.Done()
			return
		}

		if !request.NoReply() {

			// unmarshal into json array
			changesBatch := [][]interface{}{}

			if err := json.Unmarshal(body, &changesBatch); err != nil {
				panic(fmt.Sprintf("Error unmarshalling changes. Body: %vs.  Error: %v", string(body), err))
			}

			responseVal := [][]interface{}{}
			for _, change := range changesBatch {
				revId := change[2].(string)
				responseVal = append(responseVal, []interface{}{revId})
				revsFinishedWg.Add(1)
			}

			response := request.Response()
			responseValBytes, err := json.Marshal(responseVal)
			log.Printf("responseValBytes: %s", responseValBytes)
			if err != nil {
				panic(fmt.Sprintf("Error marshalling response: %v", err))
			}
			response.SetBody(responseValBytes)

		}
	}

	// -------- Rev handler callback --------
	bt.blipContext.HandlerForProfile["rev"] = func(request *blip.Message) {

		defer revsFinishedWg.Done()
		body, err := request.Body()
		var doc rest.RestDocument
		err = json.Unmarshal(body, &doc)
		if err != nil {
			panic(fmt.Sprintf("Unexpected err: %v", err))
		}
		docId := request.Properties["id"]
		docRev := request.Properties["rev"]
		doc.SetID(docId)
		doc.SetRevID(docRev)

		docsLock.Lock()
		docs[docId] = doc
		docsLock.Unlock()

		attachments, err := doc.GetAttachments()
		if err != nil {
			panic(fmt.Sprintf("Unexpected err: %v", err))
		}

		for _, attachment := range attachments {

			// Get attachments and append to RestDocument
			getAttachmentRequest := blip.NewRequest()
			getAttachmentRequest.SetProfile(db.MessageGetAttachment)
			getAttachmentRequest.Properties[db.GetAttachmentDigest] = attachment.Digest
			if bt.blipContext.ActiveSubprotocol() == db.BlipCBMobileReplicationV3 {
				getAttachmentRequest.Properties[db.GetAttachmentID] = docId
			}
			sent := bt.sender.Send(getAttachmentRequest)
			if !sent {
				panic(fmt.Sprintf("Unable to get attachment."))
			}
			getAttachmentResponse := getAttachmentRequest.Response()
			getAttachmentBody, getAttachmentErr := getAttachmentResponse.Body()
			if getAttachmentErr != nil {
				panic(fmt.Sprintf("Unexpected err: %v", err))
			}
			log.Printf("getAttachmentBody: %s", getAttachmentBody)
			attachment.Data = getAttachmentBody
		}

		// Send response to rev request
		if !request.NoReply() {
			response := request.Response()
			response.SetBody([]byte{}) // Empty response to indicate success
		}

	}

	// -------- Norev handler callback --------
	bt.blipContext.HandlerForProfile["norev"] = func(request *blip.Message) {
		// If a norev is received, then don't bother waiting for one of the expected revisions, since it will never come.
		// The norev could be added to the returned docs map, but so far there is no need for that.  The ability
		// to assert on the number of actually received revisions (which norevs won't affect) meets current test requirements.
		defer revsFinishedWg.Done()
	}

	// Send subChanges to subscribe to changes, which will cause the "changes" profile handler above to be called back
	changesFinishedWg.Add(1)
	subChangesRequest := blip.NewRequest()
	subChangesRequest.SetProfile("subChanges")
	subChangesRequest.Properties["continuous"] = "false"

	sent := bt.sender.Send(subChangesRequest)
	if !sent {
		panic(fmt.Sprintf("Unable to subscribe to changes."))
	}

	changesFinishedWg.Wait()

	revsFinishedWg.Wait()

	return docs

}

func (bt *BlipTester) SubscribeToChanges(continuous bool, changes chan<- *blip.Message) {

	// When this test sends subChanges, Sync Gateway will send a changes request that must be handled
	bt.blipContext.HandlerForProfile["changes"] = func(request *blip.Message) {

		changes <- request

		if !request.NoReply() {
			// Send an empty response to avoid the Sync: Invalid response to 'changes' message
			response := request.Response()
			emptyResponseVal := []interface{}{}
			emptyResponseValBytes, err := json.Marshal(emptyResponseVal)
			if err != nil {
				panic(fmt.Sprintf("Error marshalling response: %v", err))
			}
			response.SetBody(emptyResponseValBytes)
		}

	}

	// Send subChanges to subscribe to changes, which will cause the "changes" profile handler above to be called back
	subChangesRequest := blip.NewRequest()
	subChangesRequest.SetProfile("subChanges")
	switch continuous {
	case true:
		subChangesRequest.Properties["continuous"] = "true"
	default:
		subChangesRequest.Properties["continuous"] = "false"
	}

	sent := bt.sender.Send(subChangesRequest)
	if !sent {
		panic(fmt.Sprintf("Unable to subscribe to changes."))
	}
	subChangesResponse := subChangesRequest.Response()
	if subChangesResponse.SerialNumber() != subChangesRequest.SerialNumber() {
		panic(fmt.Sprintf("subChangesResponse.SerialNumber() != subChangesRequest.SerialNumber().  %v != %v", subChangesResponse.SerialNumber(), subChangesRequest.SerialNumber()))
	}

}
