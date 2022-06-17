package base

import (
	"crypto/x509"
	"errors"
	"io/ioutil"

	"github.com/couchbase/sync_gateway/logger"
)

func TLSRootCAProvider(tlsSkipVerify bool, caCertPath string) (wrapper func() *x509.CertPool, err error) {
	var certPool *x509.CertPool = nil
	if !tlsSkipVerify { // Add certs if ServerTLSSkipVerify is not set
		certPool, err = getRootCAs(caCertPath)
		if err != nil {
			return nil, err
		}
	}

	return func() *x509.CertPool {
		return certPool
	}, nil
}

// getRootCAs gets generates a cert pool from the certs at caCertPath. If caCertPath is empty, the systems cert pool is used.
// If an error happens when retrieving the system cert pool, it is logged (not returned) and an empty (not nil) cert pool is returned.
func getRootCAs(caCertPath string) (*x509.CertPool, error) {
	if caCertPath != "" {
		rootCAs := x509.NewCertPool()

		caCert, err := ioutil.ReadFile(caCertPath)
		if err != nil {
			return nil, err
		}

		ok := rootCAs.AppendCertsFromPEM(caCert)
		if !ok {
			return nil, errors.New("invalid CA cert")
		}

		return rootCAs, nil
	}

	rootCAs, err := x509.SystemCertPool()
	if err != nil {
		rootCAs = x509.NewCertPool()
		logger.For(logger.SystemKey).Warn().Err(err).Msgf("Could not retrieve root CAs: %v", err)
	}
	return rootCAs, nil
}
