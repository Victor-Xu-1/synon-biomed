package server

import (
	"io"
	"net/http"
	"net/http/httptest"
)

func newLoopbackTestRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "localhost"
	return request
}
