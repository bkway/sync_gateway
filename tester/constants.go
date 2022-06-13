package tester

import (
	"os"

	"github.com/couchbase/sync_gateway/base"
)

// UnitTestUrl returns the configured test URL.
func UnitTestUrl() string {
	if TestUseCouchbaseServer() {
		testCouchbaseServerUrl := os.Getenv(TestEnvCouchbaseServerUrl)
		if testCouchbaseServerUrl != "" {
			// If user explicitly set a Test Couchbase Server URL, use that
			return testCouchbaseServerUrl
		}
		// Otherwise fallback to hardcoded default
		return kTestCouchbaseServerURL
	} else {
		return kTestWalrusURL
	}
}

// UnitTestUrlIsWalrus returns true if we're running with a Walrus test URL.
func UnitTestUrlIsWalrus() bool {
	return base.ServerIsWalrus(UnitTestUrl())
}

// Couchbase 5.x notes:
// For every bucket that the tests will create (DefaultTestBucketname, DefaultTestIndexBucketname):
//   1. Create an RBAC user with username equal to the bucket name
//   2. Set the password to DefaultTestPassword
//   3. Give "Admin" RBAC rights

const (
	kTestCouchbaseServerURL = "couchbase://localhost"
	kTestWalrusURL          = "walrus:"

	DefaultTestBucketname = "test_data_bucket"
	DefaultTestUsername   = DefaultTestBucketname
	DefaultTestPassword   = "password"

	DefaultTestIndexBucketname = "test_indexbucket"
	DefaultTestIndexUsername   = DefaultTestIndexBucketname
	DefaultTestIndexPassword   = DefaultTestPassword

	// Env variable to enable user to override the Couchbase Server URL used in tests
	TestEnvCouchbaseServerUrl = "SG_TEST_COUCHBASE_SERVER_URL"

	// Env variable to enable skipping of TLS certificate verification for client and server
	TestEnvTLSSkipVerify     = "SG_TEST_TLS_SKIP_VERIFY"
	DefaultTestTLSSkipVerify = true

	// Walrus by default, but can set to "Couchbase" to have it use http://localhost:8091
	TestEnvSyncGatewayBackingStore = "SG_TEST_BACKING_STORE"
	TestEnvBackingStoreCouchbase   = "Couchbase"

	// Don't use Xattrs by default, but provide the test runner a way to specify Xattr usage
	TestEnvSyncGatewayUseXattrs = "SG_TEST_USE_XATTRS"
	TestEnvSyncGatewayTrue      = "True"

	// Should the tests drop the GSI indexes?
	TestEnvSyncGatewayDropIndexes = "SG_TEST_DROP_INDEXES"

	// Should the tests use GSI instead of views?
	TestEnvSyncGatewayDisableGSI = "SG_TEST_USE_GSI"

	// Don't use an auth handler by default, but provide a way to override
	TestEnvSyncGatewayUseAuthHandler = "SG_TEST_USE_AUTH_HANDLER"

	// Can be used to set a global log level for all tests at runtime.
	TestEnvGlobalLogLevel = "SG_TEST_LOG_LEVEL"

	// Should x509 tests deploy certs to local macOS Couchbase Server?
	TestEnvX509Local = "SG_TEST_X509_LOCAL"

	// If TestEnvX509Local=true, must use SG_TEST_X509_LOCAL_USER to set macOS username to locate CBS cert inbox
	TestEnvX509LocalUser = "SG_TEST_X509_LOCAL_USER"
)
