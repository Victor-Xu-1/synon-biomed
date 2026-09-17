package server

import (
	"bytes"
	"net/http"
)

func postWebhookStatus(url string, payload []byte) (int, error) {
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
