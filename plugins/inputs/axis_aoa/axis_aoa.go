package axis_aoa

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/plugins/inputs"
)

//go:embed sample.conf
var sampleConfig string

const (
	defaultAPIVersion = "1.2"
	defaultTimeout    = config.Duration(10 * time.Second)

	controlPath = "/local/objectanalytics/control.cgi"
	serialPath  = "/axis-cgi/param.cgi?action=list&group=Properties.System.SerialNumber"

	// Scenario types that expose counting data.
	typeCrosslineCounting = "crosslinecounting"
	typeOccupancyInArea   = "occupancyInArea"
)

// AxisAOA is a Telegraf input plugin that collects counting data from AXIS
// Object Analytics (AOA) Crossline Counting and Occupancy in Area scenarios
// through the VAPIX Object Analytics API.
type AxisAOA struct {
	URL        string          `toml:"url"`
	Username   config.Secret   `toml:"username"`
	Password   config.Secret   `toml:"password"`
	APIVersion string          `toml:"api_version"`
	Scenarios  []int           `toml:"scenarios"`
	Serial     string          `toml:"serial"`
	Timeout    config.Duration `toml:"timeout"`

	InsecureSkipVerify bool `toml:"insecure_skip_verify"`

	Log telegraf.Logger `toml:"-"`

	client  *http.Client
	baseURL string

	// Resolved device serial number, cached after the first successful lookup.
	serial string
}

// scenario is a counting-capable scenario discovered from the configuration.
type scenario struct {
	ID   int
	Name string
	Type string
}

// controlResponse is the common envelope returned by control.cgi methods.
type controlResponse struct {
	APIVersion string          `json:"apiVersion"`
	Context    string          `json:"context"`
	Method     string          `json:"method"`
	Data       json.RawMessage `json:"data"`
	Error      *controlError   `json:"error"`
}

type controlError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type configurationData struct {
	Scenarios []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"scenarios"`
}

func (*AxisAOA) SampleConfig() string {
	return sampleConfig
}

func (a *AxisAOA) Init() error {
	if a.URL == "" {
		return errors.New("\"url\" is required")
	}
	a.baseURL = strings.TrimRight(a.URL, "/")

	if a.APIVersion == "" {
		a.APIVersion = defaultAPIVersion
	}
	if a.Timeout <= 0 {
		a.Timeout = defaultTimeout
	}

	username, err := a.Username.Get()
	if err != nil {
		return fmt.Errorf("getting username failed: %w", err)
	}
	defer username.Destroy()
	password, err := a.Password.Get()
	if err != nil {
		return fmt.Errorf("getting password failed: %w", err)
	}
	defer password.Destroy()

	a.client = &http.Client{
		Timeout: time.Duration(a.Timeout),
		Transport: newDigestTransport(
			username.String(),
			password.String(),
			a.InsecureSkipVerify,
		),
	}

	return nil
}

func (a *AxisAOA) Gather(acc telegraf.Accumulator) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(a.Timeout))
	defer cancel()

	scenarios, err := a.discoverScenarios(ctx)
	if err != nil {
		return err
	}
	if len(scenarios) == 0 {
		a.Log.Debug("no counting scenarios found")
		return nil
	}

	serial := a.resolveSerial(ctx)

	for _, s := range scenarios {
		if err := a.gatherScenario(ctx, acc, s, serial); err != nil {
			acc.AddError(fmt.Errorf("scenario %d (%s): %w", s.ID, s.Name, err))
		}
	}

	return nil
}

// discoverScenarios returns the counting-capable scenarios to query. When the
// scenarios config option is set, those IDs are looked up in the device
// configuration; otherwise every Crossline Counting and Occupancy in Area
// scenario is returned.
func (a *AxisAOA) discoverScenarios(ctx context.Context) ([]scenario, error) {
	cfg, err := a.getConfiguration(ctx)
	if err != nil {
		return nil, err
	}

	wanted := make(map[int]bool, len(a.Scenarios))
	for _, id := range a.Scenarios {
		wanted[id] = true
	}

	var result []scenario
	for _, s := range cfg.Scenarios {
		if len(wanted) > 0 {
			if !wanted[s.ID] {
				continue
			}
		} else if s.Type != typeCrosslineCounting && s.Type != typeOccupancyInArea {
			continue
		}
		result = append(result, scenario{ID: s.ID, Name: s.Name, Type: s.Type})
	}
	return result, nil
}

func (a *AxisAOA) getConfiguration(ctx context.Context) (configurationData, error) {
	resp, err := a.call(ctx, "getConfiguration", nil)
	if err != nil {
		return configurationData{}, err
	}
	var cfg configurationData
	if err := json.Unmarshal(resp.Data, &cfg); err != nil {
		return configurationData{}, fmt.Errorf("decoding configuration failed: %w", err)
	}
	return cfg, nil
}

// gatherScenario reads the counting data for a single scenario and emits one
// metric. The method used depends on the scenario type.
func (a *AxisAOA) gatherScenario(ctx context.Context, acc telegraf.Accumulator, s scenario, serial string) error {
	method := "getAccumulatedCounts"
	if s.Type == typeOccupancyInArea {
		method = "getOccupancy"
	}

	resp, err := a.call(ctx, method, map[string]interface{}{"scenario": s.ID})
	if err != nil {
		return err
	}

	fields, ts, err := parseCounts(resp.Data)
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		return nil
	}

	tags := map[string]string{
		"source":        a.host(),
		"scenario_id":   strconv.Itoa(s.ID),
		"scenario_name": s.Name,
		"scenario_type": s.Type,
	}
	if serial != "" {
		tags["serial"] = serial
	}

	if ts.IsZero() {
		ts = time.Now()
	}
	acc.AddFields("axis_aoa", fields, tags, ts)
	return nil
}

// countTimeLayouts lists the timestamp formats observed across devices and the
// API documentation. The device may return millisecond precision and a numeric
// timezone offset without a colon, which is not valid RFC 3339.
var countTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05.000Z0700",
	"2006-01-02T15:04:05Z0700",
}

// parseCounts converts a counting payload (from getAccumulatedCounts or
// getOccupancy) into metric fields and the read timestamp. The total and any
// per-category counts are emitted as numeric fields; the reset time, when
// present, is emitted as a string field.
func parseCounts(raw json.RawMessage) (map[string]interface{}, time.Time, error) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, time.Time{}, fmt.Errorf("decoding counts failed: %w", err)
	}

	fields := make(map[string]interface{}, len(data))
	var ts time.Time

	for key, rawVal := range data {
		// The key casing varies across firmware (e.g. "timeStamp" vs
		// "timestamp"), so compare case-insensitively.
		switch strings.ToLower(key) {
		case "timestamp":
			if t, ok := parseCountTime(rawVal); ok {
				ts = t
			}
		case "resettime":
			var s string
			if err := json.Unmarshal(rawVal, &s); err == nil && s != "" {
				fields["reset_time"] = s
			}
		default:
			value, ok := parseNumber(rawVal)
			if !ok {
				continue
			}
			fields[fieldName(key)] = value
		}
	}

	return fields, ts, nil
}

// parseCountTime parses a device timestamp string using the known layouts.
func parseCountTime(raw json.RawMessage) (time.Time, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s == "" {
		return time.Time{}, false
	}
	for _, layout := range countTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseNumber decodes a JSON value that may be a number or a numeric string
// into a float64.
func parseNumber(raw json.RawMessage) (float64, bool) {
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// fieldName converts an AOA category key (e.g. "otherVehicle") into a snake_case
// metric field name (e.g. "other_vehicle").
func fieldName(key string) string {
	var b strings.Builder
	for i, r := range key {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// call performs a control.cgi POST for the given method and returns the parsed
// response envelope, surfacing device-reported errors.
func (a *AxisAOA) call(ctx context.Context, method string, params map[string]interface{}) (*controlResponse, error) {
	payload := map[string]interface{}{
		"apiVersion": a.APIVersion,
		"context":    "telegraf",
		"method":     method,
	}
	if params != nil {
		payload["params"] = params
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+controlPath, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	body, err := a.do(req)
	if err != nil {
		return nil, err
	}

	var resp controlResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decoding %s response failed: %w", method, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("device error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return &resp, nil
}

// resolveSerial returns the device serial number used as the stable `serial`
// tag. A configured value takes precedence; otherwise it is fetched from the
// device once and cached. Lookup failures are non-fatal and yield an empty
// string so the tag is simply omitted.
func (a *AxisAOA) resolveSerial(ctx context.Context) string {
	if a.serial != "" {
		return a.serial
	}
	if a.Serial != "" {
		a.serial = a.Serial
		return a.serial
	}
	serial, err := a.deviceSerial(ctx)
	if err != nil {
		a.Log.Debugf("serial discovery failed: %v", err)
		return ""
	}
	a.serial = serial
	return serial
}

// deviceSerial fetches the serial number from the device parameter API. The
// response is a single `Properties.System.SerialNumber=<serial>` line.
func (a *AxisAOA) deviceSerial(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+serialPath, nil)
	if err != nil {
		return "", err
	}

	body, err := a.do(req)
	if err != nil {
		return "", err
	}

	line := strings.TrimSpace(string(body))
	_, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", fmt.Errorf("unexpected serial response %q", line)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("device reported empty serial number")
	}
	return value, nil
}

func (a *AxisAOA) do(req *http.Request) ([]byte, error) {
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (a *AxisAOA) host() string {
	if u, err := url.Parse(a.baseURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return a.baseURL
}

func init() {
	inputs.Add("axis_aoa", func() telegraf.Input {
		return &AxisAOA{
			APIVersion: defaultAPIVersion,
			Timeout:    defaultTimeout,
		}
	})
}
