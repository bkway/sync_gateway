package tester

import (
	"context"
	"fmt"
	"time"

	"github.com/couchbase/gocb/v2"
	"github.com/couchbase/sync_gateway/base"
	"github.com/couchbase/sync_gateway/logger"
)

type clusterLogFunc func(ctx context.Context, format string, args ...interface{})

// tbpCluster defines the required test bucket pool cluster operations
type tbpCluster interface {
	getBucketNames() ([]string, error)
	insertBucket(name string, quotaMB int) error
	removeBucket(name string) error
	openTestBucket(name tbpBucketName, waitUntilReadySeconds int) (base.Bucket, error)
	close() error
}

// newTestCluster returns a cluster based on the driver used by the defaultBucketSpec.  Accepts a clusterLogFunc to support
// cluster logging within a test bucket pool context
func newTestCluster(server string, logger clusterLogFunc) tbpCluster {
	// if tbpDefaultBucketSpec.CouchbaseDriver == base.GoCBv2 {
	return newTestClusterV2(server, logger)
	// }
}

// tbpClusterV2 implements the tbpCluster interface for a gocb v2 cluster
type tbpClusterV2 struct {
	logger clusterLogFunc
	server string
}

var _ tbpCluster = &tbpClusterV2{}

func newTestClusterV2(server string, logger clusterLogFunc) *tbpClusterV2 {
	tbpCluster := &tbpClusterV2{}
	tbpCluster.logger = logger
	tbpCluster.server = server
	return tbpCluster
}

// getCluster makes cluster connection.  Callers must close.
func getCluster(server string) *gocb.Cluster {

	testClusterTimeout := 10 * time.Second
	spec := base.BucketSpec{
		Server:          server,
		TLSSkipVerify:   true,
		BucketOpTimeout: &testClusterTimeout,
		CouchbaseDriver: TestClusterDriver(),
	}

	connStr, err := spec.GetGoCBConnString()
	if err != nil {
		logger.For(logger.UnknownKey).Fatal().Err(err).Msg("error getting connection string")
	}

	securityConfig, err := base.GoCBv2SecurityConfig(&spec.TLSSkipVerify, spec.CACertPath)
	if err != nil {
		logger.For(logger.UnknownKey).Fatal().Err(err).Msg("Couldn't initialize cluster security config")
	}

	authenticatorConfig, authErr := base.GoCBv2Authenticator(TestClusterUsername(), TestClusterPassword(), spec.Certpath, spec.Keypath)
	if authErr != nil {
		logger.For(logger.UnknownKey).Fatal().Err(authErr).Msg("Couldn't initialize cluster authenticator config")
	}

	timeoutsConfig := base.GoCBv2TimeoutsConfig(spec.BucketOpTimeout, base.StdlibDurationPtr(spec.GetViewQueryTimeout()))

	clusterOptions := gocb.ClusterOptions{
		Authenticator:  authenticatorConfig,
		SecurityConfig: securityConfig,
		TimeoutsConfig: timeoutsConfig,
	}

	cluster, err := gocb.Connect(connStr, clusterOptions)
	if err != nil {
		logger.For(logger.UnknownKey).Fatal().Err(err).Msgf("Couldn't connect to %q", server)
	}
	err = cluster.WaitUntilReady(15*time.Second, nil)
	if err != nil {
		logger.For(logger.UnknownKey).Fatal().Err(err).Msg("Cluster not ready")
	}

	return cluster
}

func (c *tbpClusterV2) getBucketNames() ([]string, error) {

	cluster := getCluster(c.server)
	defer c.closeCluster(cluster)

	manager := cluster.Buckets()

	bucketSettings, err := manager.GetAllBuckets(nil)
	if err != nil {
		return nil, fmt.Errorf("couldn't get buckets from cluster: %w", err)
	}

	var names []string
	for name := range bucketSettings {
		names = append(names, name)
	}

	return names, nil
}

func (c *tbpClusterV2) insertBucket(name string, quotaMB int) error {

	cluster := getCluster(c.server)
	defer c.closeCluster(cluster)
	settings := gocb.CreateBucketSettings{
		BucketSettings: gocb.BucketSettings{
			Name:         name,
			RAMQuotaMB:   uint64(quotaMB),
			BucketType:   gocb.CouchbaseBucketType,
			FlushEnabled: true,
			NumReplicas:  0,
		},
	}

	options := &gocb.CreateBucketOptions{
		Timeout: 10 * time.Second,
	}
	return cluster.Buckets().CreateBucket(settings, options)
}

func (c *tbpClusterV2) removeBucket(name string) error {
	cluster := getCluster(c.server)
	defer c.closeCluster(cluster)

	return cluster.Buckets().DropBucket(name, nil)
}

// openTestBucket opens the bucket of the given name for the gocb cluster in the given TestBucketPool.
func (c *tbpClusterV2) openTestBucket(testBucketName tbpBucketName, waitUntilReadySeconds int) (base.Bucket, error) {

	cluster := getCluster(c.server)
	bucketSpec := getBucketSpec(testBucketName)

	return base.GetCollectionFromCluster(cluster, bucketSpec, waitUntilReadySeconds)
}

func (c *tbpClusterV2) close() error {
	// no close operations needed
	return nil
}

func (c *tbpClusterV2) closeCluster(cluster *gocb.Cluster) {
	if err := cluster.Close(nil); err != nil {
		c.logger(context.Background(), "Couldn't close cluster connection: %v", err)
	}
}
