# telegraf-execd-axis-aoa

A [Telegraf external plugin][external] that collects counting data from
[AXIS Object Analytics][aoa] (AOA) scenarios using the VAPIX
[Object Analytics API][api] and feeds them into Telegraf through the
[`inputs.execd`][execd] plugin. From there Telegraf can write to InfluxDB or any
other supported output.

[external]: https://github.com/influxdata/telegraf/blob/master/docs/EXTERNAL_PLUGINS.md
[execd]: https://github.com/influxdata/telegraf/tree/master/plugins/inputs/execd
[aoa]: https://www.axis.com/products/axis-object-analytics
[api]: https://developer.axis.com/vapix/applications/axis-object-analytics-api/

It collects two kinds of counting data:

- **Crossline Counting** scenarios via `getAccumulatedCounts`: the accumulated
  number of objects that have crossed the counting line since the last reset,
  per object class (human, car, bus, bike, truck, other vehicle).
- **Occupancy in Area** scenarios via `getOccupancy`: the number of objects
  currently in the area, per object class.

> This is a sibling of [telegraf-execd-axis-airquality][aq], which collects air
> quality measurements from Axis environmental sensors. The two share the same
> structure and digest-auth approach.

[aq]: https://github.com/Mo3he/telegraf-execd-axis-airquality

## Why this instead of `mqtt_consumer` + `json_v2`?

A common DIY approach is to publish AOA events to MQTT and parse them in
Telegraf with the generic `mqtt_consumer` input and a `json_v2` parser. That
works for event-driven line-crossing pulses, but it puts the burden on you to
hand-map every JSON path, and it does not give you the device's own
**accumulated** totals or current occupancy on demand.

This plugin talks to the device over VAPIX, asks AOA directly for the
accumulated counts and occupancy, emits clean Telegraf line protocol
(`data_format = "influx"`), and **auto-discovers the device serial** as a stable
`serial` tag that survives IP changes. There is no `json_v2` block to write and
no scenario IDs to guess.

## Metrics

See the plugin [README](plugins/inputs/axis_aoa/README.md) for the full list of
tags and fields.

## Install

Install the prebuilt binary with Go (requires Go 1.26 or newer):

```sh
go install github.com/Mo3he/telegraf-execd-axis-aoa/cmd/axis_aoa@latest
```

This installs an `axis_aoa` executable into `$(go env GOPATH)/bin`. Make sure
that directory is on your `PATH`, then point your Telegraf `execd` config at it.

Alternatively, clone the repository and build from source (see [Build](#build)).

## Build

```sh
go build -o axis_aoa ./cmd/axis_aoa
```

This produces a standalone `axis_aoa` binary.

## Configure the plugin

Edit `plugin.conf` (read by the standalone binary, not by Telegraf directly):

```toml
[[inputs.axis_aoa]]
  url = "http://10.0.0.10"
  username = "root"
  password = "secret"
```

`${VAR}` references are expanded from the environment when the config loads, so
keep secrets out of shared files by supplying them via environment variables:

```toml
[[inputs.axis_aoa]]
  url = "http://10.0.0.10"
  username = "${AXIS_USERNAME}"
  password = "${AXIS_PASSWORD}"
```

You can verify the binary on its own. It speaks Telegraf's line protocol on
stdout. Trigger one collection cycle and exit:

```sh
echo | ./axis_aoa -config plugin.conf -poll_interval_disabled
```

Once it is wired into Telegraf, run Telegraf in test mode to see exactly what
line protocol is produced without writing anything to your output:

```sh
telegraf --config telegraf.conf --test
```

## Run with Telegraf

Add an `execd` input to your `telegraf.conf`, pointing at the built binary and
its config. Let Telegraf drive collection via STDIN on its own `interval`:

`telegraf.conf`:

```toml
[[inputs.execd]]
  command = ["/path/to/axis_aoa", "-config", "/path/to/plugin.conf", "-poll_interval_disabled"]
  signal = "STDIN"
  interval = "30s"
  data_format = "influx"

# Example: write to InfluxDB 2.x
[[outputs.influxdb_v2]]
  urls = ["http://10.0.0.20:8086"]
  token = "${INFLUX_TOKEN}"
  organization = "my-org"
  bucket = "analytics"
```

> Alternatively, the binary can self-schedule with `-poll_interval 30s` and
> `signal = "none"` instead of STDIN.

Accumulated counts grow monotonically until the scenario is reset (optionally at
midnight, if timed reset is enabled on the device). Occupancy reflects the
current count at poll time, so its resolution is your `interval`.

### Writing to InfluxDB 3

InfluxDB 3 (Core/Enterprise, default port `8181`) has no native v3 output
plugin yet; use the same `influxdb_v2` output against its v2-compatible write
API. A few fields mean different things in v3:

- `bucket` maps to the **database** name, so set it to the database you created.
- `organization` is ignored by v3 and can be any non-empty placeholder.
- `token` must be an InfluxDB 3 token with write permission on that database.

```toml
[[outputs.influxdb_v2]]
  urls = ["http://10.0.0.20:8181"]
  token = "${INFLUX_TOKEN}"
  organization = "placeholder"   # ignored by InfluxDB 3
  bucket = "analytics"           # this is the v3 database name
```

> InfluxDB 2.x uses token/org/bucket authentication. Create an API token in the
> InfluxDB UI and supply it via the `token` field. Prefer an environment
> variable (`token = "${INFLUX_TOKEN}"`) over an inline secret so credentials
> stay out of shared config files.
