# Okta Receiver
This receiver collects system logs from an Okta domain.

## Minimum Agent Versions
- Introduced: [v1.59.0](https://github.com/observIQ/bindplane-otel-collector/releases/tag/v1.59.0)

## Supported Pipelines
- Logs

## How It Works
1. The receiver polls the Okta [System Log API](https://developer.okta.com/docs/reference/api/system-log/) once per `poll_interval` for events published since the previous poll.
2. The receiver follows pagination links to retrieve all events within the poll window.
3. The receiver converts each event to an OpenTelemetry log and sends it to the collector.
   - Key event fields (such as UUID, event type, outcome, and actor details) are promoted to log attributes, and the Okta domain is set as a resource attribute.

## Prerequisites
- An Okta API Token will be needed to authorize the receiver with your Okta Domain.

## Configuration
| Field                | Type      | Default          | Required | Description                                                                                                                                                                                                           |
|----------------------|-----------|------------------|----------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| okta_domain          |  string   |                  | `true`   | The Okta domain the receiver should collect logs from (Do not include "https://"): [Find your Okta Domain](https://developer.okta.com/docs/guides/find-your-domain/main/)                                             |
| api_token            |  string   |                  | `true`   | An Okta API Token generated from the above Okta domain: [How to Create an Okta API Token](https://support.okta.com/help/s/article/How-to-create-an-API-token?language=en_US)                                          |
| poll_interval        |  string   | 1m               | `false`  | The rate at which this receiver will poll Okta for logs. This value must be in the range [1 second - 24 hours] and must be a string readable by Golang's [time.ParseDuration](https://pkg.go.dev/time#ParseDuration). |

### Example Configuration
```yaml
receivers:
  okta:
    okta_domain: example.okta.com
    api_token: myAPIToken
    poll_interval: 2m
```
