# Axis Object Analytics Input Plugin

Collects counting data from [AXIS Object Analytics][aoa] (AOA) scenarios through
the VAPIX [Object Analytics API][api].

It polls the device `control.cgi` endpoint and emits one metric per
counting-capable scenario:

- **Crossline Counting** scenarios are read with `getAccumulatedCounts`, which
  returns the accumulated number of objects that have crossed the counting line
  since the last reset, broken down by object class.
- **Occupancy in Area** scenarios are read with `getOccupancy`, which returns
  the number of objects currently in the area, broken down by object class.

Scenarios are discovered automatically from the device configuration, so no
scenario IDs need to be configured.

[aoa]: https://www.axis.com/products/axis-object-analytics
[api]: https://developer.axis.com/vapix/applications/axis-object-analytics-api/

## Requirements

- An Axis device running AXIS Object Analytics with at least one Crossline
  Counting or Occupancy in Area scenario configured.
- A device account (digest authentication is used automatically).

## Configuration

```toml @sample.conf
# Read counting data from AXIS Object Analytics (AOA) scenarios via the VAPIX
# Object Analytics API.
[[inputs.axis_aoa]]
  ## Base URL of the Axis device. Use https:// if the device requires it.
  url = "http://10.0.0.10"

  ## Device credentials. Digest authentication is used automatically.
  ## ${VAR} references are expanded from the environment, so you can keep
  ## secrets out of the file, e.g. password = "${AXIS_PASSWORD}".
  username = "root"
  password = "secret"

  ## Object Analytics API version to use.
  # api_version = "1.2"

  ## Scenario IDs to query. When empty, every Crossline Counting and Occupancy
  ## in Area scenario found on the device is collected automatically.
  # scenarios = [1, 2]

  ## Device serial number, added as the stable "serial" tag. When empty it is
  ## fetched from the device automatically. Set it explicitly to override or to
  ## avoid the lookup.
  # serial = ""

  ## HTTP request timeout.
  # timeout = "10s"

  ## Skip TLS certificate verification (useful for self-signed device certs).
  # insecure_skip_verify = false
```

## Metrics

- `axis_aoa`
  - tags:
    - `source` (device host)
    - `serial` (device serial number; auto-discovered, stable across IP changes)
    - `scenario_id`
    - `scenario_name`
    - `scenario_type` (`crosslinecounting` or `occupancyInArea`)
  - fields:
    - `total` (float, total objects of any class)
    - one field per reported object class. The exact field names are
      firmware-dependent, e.g. `total_human` / `total_car` on some devices and
      `human` / `car` on others (camelCase keys are converted to snake_case).
      Only classes the scenario reports are emitted.
    - `reset_time` (string, Crossline Counting only: when counting last reset)

The metric timestamp is the device-reported read time. Object class fields and
the timestamp key casing vary between firmware versions; the plugin handles both
camelCase and lowercase variants.

## Example Output

```text
axis_aoa,scenario_id=2,scenario_name=Scenario\ 2,scenario_type=crosslinecounting,serial=B8A44F305363,source=10.129.174.120 total=0,total_human=0,reset_time="2026-06-11T02:29:58.000-0400" 1781159687935000000
axis_aoa,scenario_id=3,scenario_name=Lobby,scenario_type=occupancyInArea,serial=B8A44F305363,source=10.129.174.120 total=5,human=5 1781076620000000000
```
