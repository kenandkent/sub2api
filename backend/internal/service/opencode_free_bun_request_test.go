package service

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOpenCodeFreeRequestCapabilities(t *testing.T) {
	for _, body := range []string{
		`{"model":"alias","messages":[{"role":"user","content":"hello"}],"temperature":0,"tools":[],"tool_choice":"none","n":1}`,
		`{"model":"alias","messages":[{"role":"system","content":"rules"},{"role":"assistant","content":"ok"},{"role":"user","content":"next"}]}`,
	} {
		normalized, _, err := normalizeOpenCodeFreeRequest([]byte(body), "big-pickle")
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(normalized, &got))
		require.Equal(t, "big-pickle", got["model"])
	}
	for _, body := range []string{
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"temperature":0.1}`,
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{}]}`,
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"n":2}`,
		`{"model":"alias","messages":[{"role":"tool","content":"x"}]}`,
		`{"model":"alias","messages":[{"role":"user","content":[{"type":"image_url"}]}]}`,
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"functions":[]}`,
		`{"model":"alias","messages":[{"role":"user","content":"x"}],"unknown":true}`,
	} {
		_, _, err := normalizeOpenCodeFreeRequest([]byte(body), "big-pickle")
		require.Error(t, err, body)
	}
}
