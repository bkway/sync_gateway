package tester

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sgbucket "github.com/couchbase/sg-bucket"
	"github.com/couchbase/sync_gateway/auth"
	"github.com/couchbase/sync_gateway/base"
	"github.com/couchbase/sync_gateway/channels"
	"github.com/couchbase/sync_gateway/db"
	"github.com/couchbase/sync_gateway/logger"
	"github.com/couchbase/sync_gateway/rest"
	"github.com/couchbase/sync_gateway/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

type RestTesterConfig struct {
	guestEnabled                    bool                 // If this is true, Admin Party is in full effect
	SyncFn                          string               // put the sync() function source in here (optional)
	DatabaseConfig                  *rest.DatabaseConfig // Supports additional config options.  BucketConfig, Name, Sync, Unsupported will be ignored (overridden)
	InitSyncSeq                     uint64               // If specified, initializes _sync:seq on bucket creation.  Not supported when running against walrus
	EnableNoConflictsMode           bool                 // Enable no-conflicts mode.  By default, conflicts will be allowed, which is the default behavior
	distributedIndex                bool                 // Test with walrus-based index bucket
	TestBucket                      *base.TestBucket     // If set, use this bucket instead of requesting a new one.
	adminInterface                  string               // adminInterface overrides the default admin interface.
	sgReplicateEnabled              bool                 // sgReplicateManager disabled by default for RestTester
	hideProductInfo                 bool
	adminInterfaceAuthentication    bool
	metricsInterfaceAuthentication  bool
	enableAdminAuthPermissionsCheck bool
	useTLSServer                    bool // If true, TLS will be required for communications with CBS. Default: false
	persistentConfig                bool
	groupID                         *string
}

type RestTester struct {
	*RestTesterConfig
	tb                      testing.TB
	testBucket              *base.TestBucket
	bucketInitOnce          sync.Once
	bucketDone              base.AtomicBool
	RestTesterServerContext *rest.ServerContext
	AdminHandler            http.Handler
	adminHandlerOnce        sync.Once
	PublicHandler           http.Handler
	publicHandlerOnce       sync.Once
	MetricsHandler          http.Handler
	metricsHandlerOnce      sync.Once
	closed                  bool
}

func NewRestTester(tb testing.TB, restConfig *RestTesterConfig) *RestTester {
	var rt RestTester
	if tb == nil {
		panic("tester parameter cannot be nil")
	}
	rt.tb = tb
	if restConfig != nil {
		rt.RestTesterConfig = restConfig
	} else {
		rt.RestTesterConfig = &RestTesterConfig{}
	}
	return &rt
}

func (rt *RestTester) Bucket() base.Bucket {

	if rt.tb == nil {
		panic("RestTester not properly initialized please use NewRestTester function")
	} else if rt.closed {
		panic("RestTester was closed!")
	}

	if rt.testBucket != nil {
		return rt.testBucket.Bucket
	}

	// If we have a TestBucket defined on the RestTesterConfig, use that instead of requesting a new one.
	testBucket := rt.RestTesterConfig.TestBucket
	if testBucket == nil {
		testBucket = base.GetTestBucket(rt.tb)
	}
	rt.testBucket = testBucket

	if rt.InitSyncSeq > 0 {
		log.Printf("Initializing %s to %d", base.SyncSeqKey, rt.InitSyncSeq)
		_, incrErr := testBucket.Incr(base.SyncSeqKey, rt.InitSyncSeq, rt.InitSyncSeq, 0)
		if incrErr != nil {
			rt.tb.Fatalf("Error initializing %s in test bucket: %v", base.SyncSeqKey, incrErr)
		}
	}

	corsConfig := &rest.CORSConfig{
		Origin:      []string{"http://example.com", "*", "http://staging.example.com"},
		LoginOrigin: []string{"http://example.com"},
		Headers:     []string{},
		MaxAge:      1728000,
	}

	adminInterface := &rest.DefaultAdminInterface
	if rt.RestTesterConfig.adminInterface != "" {
		adminInterface = &rt.RestTesterConfig.adminInterface
	}

	sc := rest.DefaultStartupConfig("")

	username, password, _ := testBucket.BucketSpec.Auth.GetCredentials()

	// Disable config polling to avoid test flakiness and increase control of timing.
	// Rely on on-demand config fetching for consistency.
	sc.Bootstrap.ConfigUpdateFrequency = base.NewConfigDuration(0)

	sc.Bootstrap.Server = testBucket.BucketSpec.Server
	sc.Bootstrap.Username = username
	sc.Bootstrap.Password = password
	sc.API.AdminInterface = *adminInterface
	sc.API.CORS = corsConfig
	sc.API.HideProductVersion = base.BoolPtr(rt.RestTesterConfig.hideProductInfo)
	sc.DeprecatedConfig = &rest.DeprecatedConfig{Facebook: &rest.FacebookConfigLegacy{}}
	sc.API.AdminInterfaceAuthentication = &rt.adminInterfaceAuthentication
	sc.API.MetricsInterfaceAuthentication = &rt.metricsInterfaceAuthentication
	sc.API.EnableAdminAuthenticationPermissionsCheck = &rt.enableAdminAuthPermissionsCheck
	sc.Bootstrap.UseTLSServer = &rt.RestTesterConfig.useTLSServer
	sc.Bootstrap.ServerTLSSkipVerify = base.BoolPtr(base.TestTLSSkipVerify())

	if rt.RestTesterConfig.groupID != nil {
		sc.Bootstrap.ConfigGroupID = *rt.RestTesterConfig.groupID
	}

	// Allow EE-only config even in CE for testing using group IDs.
	if err := sc.validate(); err != nil {
		panic("invalid RestTester StartupConfig: " + err.Error())
	}

	// Post-validation, we can lower the bcrypt cost beyond SG limits to reduce test runtime.
	sc.Auth.BcryptCost = bcrypt.MinCost

	rt.RestTesterServerContext = rest.NewServerContext(&sc, rt.RestTesterConfig.persistentConfig)

	if !base.ServerIsWalrus(sc.Bootstrap.Server) {
		// Copy any testbucket cert info into boostrap server config
		// Required as present for X509 tests there is no way to pass this info to the bootstrap server context with a
		// RestTester directly - Should hopefully be alleviated by CBG-1460
		sc.Bootstrap.CACertPath = testBucket.BucketSpec.CACertPath
		sc.Bootstrap.X509CertPath = testBucket.BucketSpec.Certpath
		sc.Bootstrap.X509KeyPath = testBucket.BucketSpec.Keypath

		rt.testBucket.BucketSpec.TLSSkipVerify = base.TestTLSSkipVerify()

		if err := rt.RestTesterServerContext.initializeCouchbaseServerConnections(); err != nil {
			panic("Couldn't initialize Couchbase Server connection: " + err.Error())
		}
	}

	// Copy this startup config at this point into initial startup config
	err := base.DeepCopyInefficient(&rt.RestTesterServerContext.initialStartupConfig, &sc)
	if err != nil {
		rt.tb.Fatalf("Unable to copy initial startup config: %v", err)
	}

	// tests must create their own databases in persistent mode
	if !rt.persistentConfig {
		useXattrs := base.TestUseXattrs()

		if rt.DatabaseConfig == nil {
			// If no db config was passed in, create one
			rt.DatabaseConfig = &rest.DatabaseConfig{}
		}

		if base.TestsDisableGSI() {
			rt.DatabaseConfig.UseViews = base.BoolPtr(true)
		}

		// numReplicas set to 0 for test buckets, since it should assume that there may only be one indexing node.
		numReplicas := uint(0)
		rt.DatabaseConfig.NumIndexReplicas = &numReplicas

		rt.DatabaseConfig.Bucket = &testBucket.BucketSpec.BucketName
		rt.DatabaseConfig.Username = username
		rt.DatabaseConfig.Password = password
		rt.DatabaseConfig.CACertPath = testBucket.BucketSpec.CACertPath
		rt.DatabaseConfig.CertPath = testBucket.BucketSpec.Certpath
		rt.DatabaseConfig.KeyPath = testBucket.BucketSpec.Keypath
		rt.DatabaseConfig.Name = "db"
		rt.DatabaseConfig.Sync = &rt.SyncFn
		rt.DatabaseConfig.EnableXattrs = &useXattrs
		if rt.EnableNoConflictsMode {
			boolVal := false
			rt.DatabaseConfig.AllowConflicts = &boolVal
		}

		rt.DatabaseConfig.SGReplicateEnabled = base.BoolPtr(rt.RestTesterConfig.sgReplicateEnabled)

		_, err = rt.RestTesterServerContext.AddDatabaseFromConfig(*rt.DatabaseConfig)
		if err != nil {
			rt.tb.Fatalf("Error from AddDatabaseFromConfig: %v", err)
		}

		// Update the testBucket Bucket to the one associated with the database context.  The new (dbContext) bucket
		// will be closed when the rest tester closes the server context. The original bucket will be closed using the
		// testBucket's closeFn
		rt.testBucket.Bucket = rt.RestTesterServerContext.Database("db").Bucket

		if err := rt.SetAdminParty(rt.guestEnabled); err != nil {
			rt.tb.Fatalf("Error from SetAdminParty %v", err)
		}
	}

	// PostStartup (without actually waiting 5 seconds)
	close(rt.RestTesterServerContext.hasStarted)

	return rt.testBucket.Bucket
}

func (rt *RestTester) ServerContext() *rest.ServerContext {
	rt.Bucket()
	return rt.RestTesterServerContext
}

// CreateDatabase is a utility function to create a database through the REST API
func (rt *RestTester) CreateDatabase(dbName string, config rest.DbConfig) (*TestResponse, error) {
	dbcJSON, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	resp := rt.SendAdminRequest(http.MethodPut, fmt.Sprintf("/%s/", dbName), string(dbcJSON))
	return resp, nil
}

// ReplaceDbConfig is a utility function to replace a database config through the REST API
func (rt *RestTester) ReplaceDbConfig(dbName string, config rest.DbConfig) (*TestResponse, error) {
	dbcJSON, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	resp := rt.SendAdminRequest(http.MethodPut, fmt.Sprintf("/%s/_config", dbName), string(dbcJSON))
	return resp, nil
}

// UpsertDbConfig is a utility function to upsert a database through the REST API
func (rt *RestTester) UpsertDbConfig(dbName string, config rest.DbConfig) (*TestResponse, error) {
	dbcJSON, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	resp := rt.SendAdminRequest(http.MethodPost, fmt.Sprintf("/%s/_config", dbName), string(dbcJSON))
	return resp, nil
}

// Returns first database found for server context.
func (rt *RestTester) GetDatabase() *db.DatabaseContext {

	for _, database := range rt.ServerContext().AllDatabases() {
		return database
	}
	return nil
}

func (rt *RestTester) MustWaitForDoc(docid string, t testing.TB) {
	err := rt.WaitForDoc(docid)
	assert.NoError(t, err)
}

func (rt *RestTester) WaitForDoc(docid string) (err error) {
	seq, err := rt.SequenceForDoc(docid)
	if err != nil {
		return err
	}
	return rt.WaitForSequence(seq)
}

func (rt *RestTester) SequenceForDoc(docid string) (seq uint64, err error) {
	database := rt.GetDatabase()
	if database == nil {
		return 0, fmt.Errorf("No database found")
	}
	doc, err := database.GetDocument(logger.TestCtx(rt.tb), docid, db.DocUnmarshalAll)
	if err != nil {
		return 0, err
	}
	return doc.Sequence, nil
}

// Wait for sequence to be buffered by the channel cache
func (rt *RestTester) WaitForSequence(seq uint64) error {
	database := rt.GetDatabase()
	if database == nil {
		return fmt.Errorf("No database found")
	}
	return database.WaitForSequence(logger.TestCtx(rt.tb), seq)
}

func (rt *RestTester) WaitForPendingChanges() error {
	database := rt.GetDatabase()
	if database == nil {
		return fmt.Errorf("No database found")
	}
	return database.WaitForPendingChanges(logger.TestCtx(rt.tb))
}

func (rt *RestTester) SetAdminParty(partyTime bool) error {
	a := rt.ServerContext().Database("db").Authenticator(logger.TestCtx(rt.tb))
	guest, err := a.GetUser("")
	if err != nil {
		return err
	}
	guest.SetDisabled(!partyTime)
	var chans channels.TimedSet
	if partyTime {
		chans = channels.AtSequence(utils.SetOf(channels.UserStarChannel), 1)
	}
	guest.SetExplicitChannels(chans, 1)
	return a.Save(guest)
}

func (rt *RestTester) Close() {
	if rt.tb == nil {
		panic("RestTester not properly initialized please use NewRestTester function")
	}
	rt.closed = true
	if rt.RestTesterServerContext != nil {
		rt.RestTesterServerContext.Close()
	}
	if rt.testBucket != nil {
		rt.testBucket.Close()
		rt.testBucket = nil
	}
}

func (rt *RestTester) SendRequest(method, resource string, body string) *TestResponse {
	return rt.Send(request(method, resource, body))
}

func (rt *RestTester) SendRequestWithHeaders(method, resource string, body string, headers map[string]string) *TestResponse {
	req := request(method, resource, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return rt.Send(req)
}

func (rt *RestTester) SendUserRequestWithHeaders(method, resource string, body string, headers map[string]string, username string, password string) *TestResponse {
	req := request(method, resource, body)
	req.SetBasicAuth(username, password)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return rt.Send(req)
}

func (rt *RestTester) SendAdminRequestWithAuth(method, resource string, body string, username string, password string) *TestResponse {
	input := bytes.NewBufferString(body)
	request, err := http.NewRequest(method, "http://localhost"+resource, input)
	require.NoError(rt.tb, err)

	request.SetBasicAuth(username, password)

	response := &TestResponse{ResponseRecorder: httptest.NewRecorder(), Req: request}
	response.Code = 200 // doesn't seem to be initialized by default; filed Go bug #4188

	rt.TestAdminHandler().ServeHTTP(response, request)
	return response
}

func (rt *RestTester) Send(request *http.Request) *TestResponse {
	response := &TestResponse{ResponseRecorder: httptest.NewRecorder(), Req: request}
	response.Code = 200 // doesn't seem to be initialized by default; filed Go bug #4188
	rt.TestPublicHandler().ServeHTTP(response, request)
	return response
}

func (rt *RestTester) TestAdminHandlerNoConflictsMode() http.Handler {
	rt.EnableNoConflictsMode = true
	return rt.TestAdminHandler()
}

func (rt *RestTester) TestAdminHandler() http.Handler {
	rt.adminHandlerOnce.Do(func() {
		rt.AdminHandler = rest.CreateAdminHandler(rt.ServerContext())
	})
	return rt.AdminHandler
}

func (rt *RestTester) TestPublicHandler() http.Handler {
	rt.publicHandlerOnce.Do(func() {
		rt.PublicHandler = rest.CreatePublicHandler(rt.ServerContext())
	})
	return rt.PublicHandler
}

func (rt *RestTester) TestMetricsHandler() http.Handler {
	rt.metricsHandlerOnce.Do(func() {
		rt.MetricsHandler = rest.CreateMetricHandler(rt.ServerContext())
	})
	return rt.MetricsHandler
}

func (rt *RestTester) CreateWaitForChangesRetryWorker(numChangesExpected int, changesUrl, username string, useAdminPort bool) (worker base.RetryWorker) {

	waitForChangesWorker := func() (shouldRetry bool, err error, value interface{}) {

		var changes changesResults
		var response *TestResponse

		if useAdminPort {
			response = rt.SendAdminRequest("GET", changesUrl, "")

		} else {
			response = rt.Send(requestByUser("GET", changesUrl, "", username))
		}
		err = json.Unmarshal(response.Body.Bytes(), &changes)
		if err != nil {
			return false, err, nil
		}
		if len(changes.Results) < numChangesExpected {
			// not enough results, retry
			return true, nil, nil
		}
		// If it made it this far, there is no errors and it got enough changes
		return false, nil, changes
	}

	return waitForChangesWorker

}

func (rt *RestTester) WaitForChanges(numChangesExpected int, changesUrl, username string, useAdminPort bool) (changes changesResults, err error) {

	waitForChangesWorker := rt.CreateWaitForChangesRetryWorker(numChangesExpected, changesUrl, username, useAdminPort)

	sleeper := base.CreateSleeperFunc(200, 100)

	err, changesVal := base.RetryLoop("Wait for changes", waitForChangesWorker, sleeper)
	if err != nil {
		return changes, err
	}

	if changesVal == nil {
		return changes, fmt.Errorf("Got nil value for changes")
	}

	if changesVal != nil {
		changes = changesVal.(changesResults)
	}

	return changes, nil
}

// WaitForCondition runs a retry loop that evaluates the provided function, and terminates
// when the function returns true.
func (rt *RestTester) WaitForCondition(successFunc func() bool) error {
	return rt.WaitForConditionWithOptions(successFunc, 200, 100)
}

func (rt *RestTester) WaitForConditionWithOptions(successFunc func() bool, maxNumAttempts, timeToSleepMs int) error {
	waitForSuccess := func() (shouldRetry bool, err error, value interface{}) {
		if successFunc() {
			return false, nil, nil
		}
		return true, nil, nil
	}

	sleeper := base.CreateSleeperFunc(maxNumAttempts, timeToSleepMs)
	err, _ := base.RetryLoop("Wait for condition options", waitForSuccess, sleeper)
	if err != nil {
		return err
	}

	return nil
}

func (rt *RestTester) WaitForConditionShouldRetry(conditionFunc func() (shouldRetry bool, err error, value interface{}), maxNumAttempts, timeToSleepMs int) error {
	sleeper := base.CreateSleeperFunc(maxNumAttempts, timeToSleepMs)
	err, _ := base.RetryLoop("Wait for condition options", conditionFunc, sleeper)
	if err != nil {
		return err
	}

	return nil
}

func (rt *RestTester) SendAdminRequest(method, resource string, body string) *TestResponse {
	input := bytes.NewBufferString(body)
	request, err := http.NewRequest(method, "http://localhost"+resource, input)
	require.NoError(rt.tb, err)

	response := &TestResponse{ResponseRecorder: httptest.NewRecorder(), Req: request}
	response.Code = 200 // doesn't seem to be initialized by default; filed Go bug #4188

	rt.TestAdminHandler().ServeHTTP(response, request)
	return response
}

func (rt *RestTester) WaitForNUserViewResults(numResultsExpected int, viewUrlPath string, user auth.User, password string) (viewResult sgbucket.ViewResult, err error) {
	return rt.WaitForNViewResults(numResultsExpected, viewUrlPath, user, password)
}

func (rt *RestTester) WaitForNAdminViewResults(numResultsExpected int, viewUrlPath string) (viewResult sgbucket.ViewResult, err error) {
	return rt.WaitForNViewResults(numResultsExpected, viewUrlPath, nil, "")
}

// Wait for a certain number of results to be returned from a view query
// viewUrlPath: is the path to the view, including the db name.  Eg: "/db/_design/foo/_view/bar"
func (rt *RestTester) WaitForNViewResults(numResultsExpected int, viewUrlPath string, user auth.User, password string) (viewResult sgbucket.ViewResult, err error) {

	worker := func() (shouldRetry bool, err error, value interface{}) {
		var response *TestResponse
		if user != nil {
			request, _ := http.NewRequest("GET", viewUrlPath, nil)
			request.SetBasicAuth(user.Name(), password)
			response = rt.Send(request)
		} else {
			response = rt.SendAdminRequest("GET", viewUrlPath, ``)
		}

		// If the view is undefined, it might be a race condition where the view is still being created
		// See https://github.com/couchbase/sync_gateway/issues/3570#issuecomment-390487982
		if strings.Contains(response.Body.String(), "view_undefined") {
			//logger.InfofCtx(response.Req.Context(), logger.KeyAll, "view_undefined error: %v.  Retrying", response.Body.String())
			logger.For(logger.AllKey).Info().Msgf("view_undefined error: %v.  Retrying", response.Body.String())
			return true, nil, nil
		}

		if response.Code != 200 {
			return false, fmt.Errorf("Got response code: %d from view call.  Expected 200.", response.Code), sgbucket.ViewResult{}
		}
		var result sgbucket.ViewResult
		_ = json.Unmarshal(response.Body.Bytes(), &result)

		if len(result.Rows) >= numResultsExpected {
			// Got enough results, break out of retry loop
			return false, nil, result
		}

		// Not enough results, retry
		return true, nil, sgbucket.ViewResult{}

	}

	description := fmt.Sprintf("Wait for %d view results for query to %v", numResultsExpected, viewUrlPath)
	sleeper := base.CreateSleeperFunc(200, 100)
	err, returnVal := base.RetryLoop(description, worker, sleeper)

	if err != nil {
		return sgbucket.ViewResult{}, err
	}

	return returnVal.(sgbucket.ViewResult), nil

}

// Waits for view to be defined on the server.  Used to avoid view_undefined errors.
func (rt *RestTester) WaitForViewAvailable(viewURLPath string) (err error) {

	worker := func() (shouldRetry bool, err error, value interface{}) {
		response := rt.SendAdminRequest("GET", viewURLPath, ``)

		if response.Code == 200 {
			return false, nil, nil
		}

		// Views unavailable, retry
		if response.Code == 500 {
			log.Printf("Error waiting for view to be available....will retry: %s", response.Body.Bytes())
			return true, fmt.Errorf("500 error"), nil
		}

		// Unexpected error, return
		return false, fmt.Errorf("Unexpected error response code while waiting for view available: %v", response.Code), nil

	}

	description := "Wait for view readiness"
	sleeper := base.CreateSleeperFunc(200, 100)
	err, _ = base.RetryLoop(description, worker, sleeper)

	return err

}

func (rt *RestTester) GetDBState() string {
	var body db.Body
	resp := rt.SendAdminRequest("GET", "/db/", "")

	assertStatus := func(t testing.TB, response *TestResponse, expectedStatus int) {
		require.Equalf(t, expectedStatus, response.Code,
			"Response status %d %q (expected %d %q)\nfor %s <%s> : %s",
			response.Code, http.StatusText(response.Code),
			expectedStatus, http.StatusText(expectedStatus),
			response.Req.Method, response.Req.URL, response.Body)
	}

	assertStatus(rt.tb, resp, 200)
	require.NoError(rt.tb, json.Unmarshal(resp.Body.Bytes(), &body))
	return body["state"].(string)
}

func (rt *RestTester) WaitForDBOnline() (err error) {
	return rt.waitForDBState("Online")
}

func (rt *RestTester) waitForDBState(stateWant string) (err error) {
	var stateCurr string
	maxTries := 20

	for i := 0; i < maxTries; i++ {
		if stateCurr = rt.GetDBState(); stateCurr == stateWant {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("given up waiting for DB state, want: %s, current: %s, attempts: %d", stateWant, stateCurr, maxTries)
}

func (rt *RestTester) SendAdminRequestWithHeaders(method, resource string, body string, headers map[string]string) *TestResponse {
	input := bytes.NewBufferString(body)
	request, _ := http.NewRequest(method, "http://localhost"+resource, input)
	for k, v := range headers {
		request.Header.Set(k, v)
	}
	response := &TestResponse{ResponseRecorder: httptest.NewRecorder(), Req: request}
	response.Code = 200 // doesn't seem to be initialized by default; filed Go bug #4188

	rt.TestAdminHandler().ServeHTTP(response, request)
	return response
}

// PutDocumentWithRevID builds a new_edits=false style put to create a revision with the specified revID.
// If parentRevID is not specified, treated as insert
func (rt *RestTester) PutDocumentWithRevID(docID string, newRevID string, parentRevID string, body db.Body) (response *rest.TestResponse, err error) {

	requestBody := body.ShallowCopy()
	newRevGeneration, newRevDigest := db.ParseRevID(newRevID)

	revisions := make(map[string]interface{})
	revisions["start"] = newRevGeneration
	ids := []string{newRevDigest}
	if parentRevID != "" {
		_, parentDigest := db.ParseRevID(parentRevID)
		ids = append(ids, parentDigest)
	}
	revisions["ids"] = ids

	requestBody[db.BodyRevisions] = revisions
	requestBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}
	resp := rt.SendAdminRequest(http.MethodPut, "/db/"+docID+"?new_edits=false", string(requestBytes))
	return resp, nil
}

// GetDocumentSequence looks up the sequence for a document using the _raw endpoint.
// Used by tests that need to validate sequences (for grants, etc)
func (rt *RestTester) GetDocumentSequence(key string) (sequence uint64) {
	response := rt.SendAdminRequest("GET", fmt.Sprintf("/db/_raw/%s", key), "")
	if response.Code != 200 {
		return 0
	}

	var rawResponse rest.RawResponse
	_ = json.Unmarshal(response.BodyBytes(), &rawResponse)
	return rawResponse.Sync.Sequence
}

type TestResponse struct {
	*httptest.ResponseRecorder
	Req *http.Request

	bodyCache []byte
}

// BodyBytes takes a copy of the bytes in the response buffer, and saves them for future callers.
func (r TestResponse) BodyBytes() []byte {
	if r.bodyCache == nil {
		r.bodyCache = r.ResponseRecorder.Body.Bytes()
	}
	return r.bodyCache
}

func (r TestResponse) DumpBody() {
	log.Printf("%v", string(r.Body.Bytes()))
}

func (r TestResponse) GetRestDocument() rest.RestDocument {
	restDoc := rest.NewRestDocument()
	err := json.Unmarshal(r.Body.Bytes(), restDoc)
	if err != nil {
		panic(fmt.Sprintf("Error parsing body into RestDocument.  Body: %s.  Err: %v", string(r.Body.Bytes()), err))
	}
	return *restDoc
}
