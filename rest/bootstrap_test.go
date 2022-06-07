package rest

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// offset for standard port numbers to avoid conflicts 4984 -> 14984
	bootstrapTestPortOffset = 10000
)

func bootstrapAdminRequest(t *testing.T, method, path, body string) *http.Response {
	url := "http://localhost:" + strconv.FormatInt(4985+bootstrapTestPortOffset, 10) + path

	buf := bytes.NewBufferString(body)
	req, err := http.NewRequest(method, url, buf)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

func bootstrapAdminRequestCustomHost(t *testing.T, method, host, path, body string) *http.Response {
	url := host + path

	buf := bytes.NewBufferString(body)
	req, err := http.NewRequest(method, url, buf)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

func bootstrapAdminRequestWithHeaders(t *testing.T, method, path, body string, headers map[string]string) *http.Response {
	url := "http://localhost:" + strconv.FormatInt(4985+bootstrapTestPortOffset, 10) + path

	buf := bytes.NewBufferString(body)
	req, err := http.NewRequest(method, url, buf)
	require.NoError(t, err)

	for headerName, headerVal := range headers {
		req.Header.Set(headerName, headerVal)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

func assertResp(t *testing.T, resp *http.Response, status int, body string) {
	assert.Equal(t, status, resp.StatusCode)
	b, _ := ioutil.ReadAll(resp.Body)
	assert.Equal(t, body, string(b))
}
