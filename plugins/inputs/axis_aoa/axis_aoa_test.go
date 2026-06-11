package axis_aoa

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/testutil"
	"github.com/stretchr/testify/require"
)

// digestServer wraps a test handler with a one-shot digest challenge so the
// digest transport is exercised end to end.
func digestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate",
				`Digest realm="AXIS_TEST", nonce="abc123", algorithm=MD5, qop="auth"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Digest ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler(w, r)
	}))
}

func newTestPlugin(t *testing.T, url string) *AxisAOA {
	t.Helper()
	plugin := &AxisAOA{
		URL:      url,
		Username: config.NewSecret([]byte("root")),
		Password: config.NewSecret([]byte("secret")),
		Log:      testutil.Logger{},
	}
	require.NoError(t, plugin.Init())
	return plugin
}

func TestInitDefaults(t *testing.T) {
	plugin := newTestPlugin(t, "http://10.0.0.10/")
	require.Equal(t, "http://10.0.0.10", plugin.baseURL)
	require.Equal(t, defaultAPIVersion, plugin.APIVersion)
	require.Equal(t, defaultTimeout, plugin.Timeout)
}

func TestInitRequiresURL(t *testing.T) {
	plugin := &AxisAOA{
		Username: config.NewSecret([]byte("root")),
		Password: config.NewSecret([]byte("secret")),
	}
	require.Error(t, plugin.Init())
}

func TestFieldName(t *testing.T) {
	require.Equal(t, "human", fieldName("human"))
	require.Equal(t, "other_vehicle", fieldName("otherVehicle"))
	require.Equal(t, "car", fieldName("car"))
}

func TestParseNumber(t *testing.T) {
	v, ok := parseNumber(json.RawMessage(`42`))
	require.True(t, ok)
	require.InDelta(t, 42.0, v, 1e-9)

	v, ok = parseNumber(json.RawMessage(`"17"`))
	require.True(t, ok)
	require.InDelta(t, 17.0, v, 1e-9)

	_, ok = parseNumber(json.RawMessage(`"notanumber"`))
	require.False(t, ok)
}

func TestParseCounts(t *testing.T) {
	raw := json.RawMessage(`{
		"resetTime": "2026-06-11T00:00:00Z",
		"timeStamp": "2026-06-11T08:30:15Z",
		"total": 124,
		"human": 100,
		"car": 24
	}`)

	fields, ts, err := parseCounts(raw)
	require.NoError(t, err)
	require.InDelta(t, 124.0, fields["total"], 1e-9)
	require.InDelta(t, 100.0, fields["human"], 1e-9)
	require.InDelta(t, 24.0, fields["car"], 1e-9)
	require.Equal(t, "2026-06-11T00:00:00Z", fields["reset_time"])
	require.Equal(t, time.Date(2026, 6, 11, 8, 30, 15, 0, time.UTC), ts)
}

// TestParseCountsDeviceFormat covers the payload shape observed on a real
// device: a lowercase "timestamp" key, millisecond precision with a numeric
// timezone offset, and "total"-prefixed class keys.
func TestParseCountsDeviceFormat(t *testing.T) {
	raw := json.RawMessage(`{
		"resetTime": "2026-06-11T02:29:58.000-0400",
		"timestamp": "2026-06-11T02:33:48.235-0400",
		"total": 0,
		"totalHuman": 0
	}`)

	fields, ts, err := parseCounts(raw)
	require.NoError(t, err)
	require.InDelta(t, 0.0, fields["total"], 1e-9)
	require.InDelta(t, 0.0, fields["total_human"], 1e-9)
	require.Equal(t, "2026-06-11T02:29:58.000-0400", fields["reset_time"])
	require.False(t, ts.IsZero())
	require.Equal(t, 2026, ts.Year())
}

func TestDiscoverScenariosAuto(t *testing.T) {
	server := digestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"apiVersion":"1.2","method":"getConfiguration","data":{
				"scenarios":[
					{"id":1,"name":"Line A","type":"crosslinecounting"},
					{"id":2,"name":"Motion","type":"motion"},
					{"id":3,"name":"Area","type":"occupancyInArea"}
				]
			}
		}`)
	})
	defer server.Close()

	plugin := newTestPlugin(t, server.URL)
	scenarios, err := plugin.discoverScenarios(t.Context())
	require.NoError(t, err)
	require.Len(t, scenarios, 2)
	require.Equal(t, 1, scenarios[0].ID)
	require.Equal(t, typeCrosslineCounting, scenarios[0].Type)
	require.Equal(t, 3, scenarios[1].ID)
	require.Equal(t, typeOccupancyInArea, scenarios[1].Type)
}

func TestDiscoverScenariosExplicit(t *testing.T) {
	server := digestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"apiVersion":"1.2","method":"getConfiguration","data":{
				"scenarios":[
					{"id":1,"name":"Line A","type":"crosslinecounting"},
					{"id":2,"name":"Motion","type":"motion"},
					{"id":3,"name":"Area","type":"occupancyInArea"}
				]
			}
		}`)
	})
	defer server.Close()

	plugin := &AxisAOA{
		URL:       server.URL,
		Scenarios: []int{2},
		Username:  config.NewSecret([]byte("root")),
		Password:  config.NewSecret([]byte("secret")),
		Log:       testutil.Logger{},
	}
	require.NoError(t, plugin.Init())

	scenarios, err := plugin.discoverScenarios(t.Context())
	require.NoError(t, err)
	require.Len(t, scenarios, 1)
	require.Equal(t, 2, scenarios[0].ID)
	require.Equal(t, "motion", scenarios[0].Type)
}

func TestGather(t *testing.T) {
	server := digestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "param.cgi") {
			_, _ = io.WriteString(w, "Properties.System.SerialNumber=E827251AA4C6\n")
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params struct {
				Scenario int `json:"scenario"`
			} `json:"params"`
		}
		require.NoError(t, json.Unmarshal(body, &req))

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "getConfiguration":
			_, _ = io.WriteString(w, `{
				"apiVersion":"1.2","method":"getConfiguration","data":{
					"scenarios":[
						{"id":1,"name":"Entrance","type":"crosslinecounting"},
						{"id":2,"name":"Lobby","type":"occupancyInArea"}
					]
				}
			}`)
		case "getAccumulatedCounts":
			_, _ = io.WriteString(w, `{
				"apiVersion":"1.2","method":"getAccumulatedCounts","data":{
					"resetTime":"2026-06-11T00:00:00Z",
					"timeStamp":"2026-06-11T08:30:15Z",
					"total":124,
					"human":100,
					"car":24
				}
			}`)
		case "getOccupancy":
			_, _ = io.WriteString(w, `{
				"apiVersion":"1.2","method":"getOccupancy","data":{
					"timeStamp":"2026-06-11T08:30:20Z",
					"total":5,
					"human":5
				}
			}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	defer server.Close()

	plugin := newTestPlugin(t, server.URL)

	var acc testutil.Accumulator
	require.NoError(t, plugin.Gather(&acc))
	require.Empty(t, acc.Errors)

	require.True(t, acc.HasMeasurement("axis_aoa"))

	crossline := findMetric(t, &acc, "1")
	require.Equal(t, "Entrance", crossline.Tags["scenario_name"])
	require.Equal(t, typeCrosslineCounting, crossline.Tags["scenario_type"])
	require.Equal(t, "E827251AA4C6", crossline.Tags["serial"])
	require.InDelta(t, 124.0, crossline.Fields["total"], 1e-9)
	require.InDelta(t, 100.0, crossline.Fields["human"], 1e-9)
	require.InDelta(t, 24.0, crossline.Fields["car"], 1e-9)
	require.Equal(t, "2026-06-11T00:00:00Z", crossline.Fields["reset_time"])
	require.Equal(t, time.Date(2026, 6, 11, 8, 30, 15, 0, time.UTC), crossline.Time)

	occupancy := findMetric(t, &acc, "2")
	require.Equal(t, typeOccupancyInArea, occupancy.Tags["scenario_type"])
	require.InDelta(t, 5.0, occupancy.Fields["total"], 1e-9)
	require.InDelta(t, 5.0, occupancy.Fields["human"], 1e-9)
}

func findMetric(t *testing.T, acc *testutil.Accumulator, scenarioID string) *testutil.Metric {
	t.Helper()
	for _, m := range acc.Metrics {
		if m.Tags["scenario_id"] == scenarioID {
			return m
		}
	}
	t.Fatalf("no metric for scenario_id %q", scenarioID)
	return nil
}

func TestCallSurfacesDeviceError(t *testing.T) {
	server := digestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"apiVersion":"1.2","method":"getConfiguration","error":{"code":2000,"message":"unsupported version"}}`)
	})
	defer server.Close()

	plugin := newTestPlugin(t, server.URL)
	_, err := plugin.getConfiguration(t.Context())
	require.ErrorContains(t, err, "device error 2000")
}

func TestSplitParams(t *testing.T) {
	params := splitParams(`realm="AXIS_TEST", nonce="a,b,c", algorithm=MD5, qop="auth"`)
	require.Equal(t, "AXIS_TEST", params["realm"])
	require.Equal(t, "a,b,c", params["nonce"])
	require.Equal(t, "MD5", params["algorithm"])
	require.Equal(t, "auth", params["qop"])
}

func TestParseChallenge(t *testing.T) {
	c := parseChallenge(`Digest realm="r", nonce="n", qop="auth", opaque="o"`)
	require.NotNil(t, c)
	require.Equal(t, "r", c.realm)
	require.Equal(t, "n", c.nonce)
	require.Equal(t, "auth", c.qop)
	require.Equal(t, "o", c.opaque)

	require.Nil(t, parseChallenge(""))
	require.Nil(t, parseChallenge("Basic realm=\"r\""))
}
