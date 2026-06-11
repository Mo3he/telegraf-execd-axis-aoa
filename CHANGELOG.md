# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-06-11

Initial release of the `axis_aoa` Telegraf external `execd` input plugin.

### Added

- Collects counting data from AXIS Object Analytics (AOA) scenarios via the
  VAPIX Object Analytics API (`control.cgi`).
- Crossline Counting support via `getAccumulatedCounts` (accumulated totals and
  per-object-class counts, plus the `reset_time` of the counter).
- Occupancy in Area support via `getOccupancy` (current objects in area and
  per-object-class counts).
- Automatic scenario discovery from `getConfiguration`; an explicit `scenarios`
  list can be configured to restrict collection.
- HTTP Digest authentication (MD5, `qop="auth"`).
- `serial` tag carrying the device serial number, auto-discovered via
  `param.cgi` and cached, or set/overridden with the `serial` config option.
- `source`, `scenario_id`, `scenario_name` and `scenario_type` tags.

### Tested with

- Verified against an Axis device (firmware AOA API 1.6) with both a Crossline
  Counting scenario (`getAccumulatedCounts`) and an Occupancy in Area scenario
  (`getOccupancy`), confirming serial discovery and counting output for both.
  The plugin tolerates the device's lowercase `timestamp` key, millisecond
  timestamps with numeric timezone offsets, and the differing class-key naming
  between methods (e.g. `total_human` for crossline vs `human` for occupancy).
- Validated end to end through Telegraf `inputs.execd` into InfluxDB v2, with
  metrics written to and read back from the target bucket.
