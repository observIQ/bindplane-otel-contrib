// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sources

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/observiq/bindplane-otel-contrib/pkg/support-util/bundle"
)

// prometheusArtifact is the JSON envelope for the metrics artifact (machine-parseable).
type prometheusArtifact struct {
	Source    string `json:"source"`
	ScrapedAt string `json:"scraped_at"`
	Format    string `json:"format"`  // "prometheus_exposition"
	Metrics   string `json:"metrics"` // raw text from the endpoint
}

// plog emits a single line of structured log (logfmt-style key=value) for machine parsing.
func plog(event string, kvs ...string) {
	var b strings.Builder
	b.WriteString("[prometheus] component=prometheus event=")
	b.WriteString(event)
	for i := 0; i+1 < len(kvs); i += 2 {
		k, v := kvs[i], kvs[i+1]
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteByte('=')
		if strings.ContainsAny(v, " \t\n\"") {
			b.WriteString(strconv.Quote(v))
		} else {
			b.WriteString(v)
		}
	}
	log.Output(2, b.String())
}

const (
	defaultPrometheusEndpoint   = "http://localhost:8888"
	defaultScrapeTimeout        = 10 * time.Second
	defaultRetryBackoffInitial  = 1 * time.Second
	defaultRetryBackoffMult     = 2.0
	defaultMaxRetryDuration     = 60 * time.Second
	defaultMaxScrapes           = 5
	maxScrapesCap               = 1000
	upCheckMaxAttempts          = 3
	upCheckTimeout              = 5 * time.Second
	upCheckDelayBetweenAttempts = 2 * time.Second
)

// PrometheusMetricsSource scrapes Prometheus metrics from an OTel Collector's metrics endpoint.
// Config is read from BundleOptions at Collect time.
type PrometheusMetricsSource struct{}

// NewPrometheusMetricsSource creates a new Prometheus metrics source.
func NewPrometheusMetricsSource() *PrometheusMetricsSource {
	return &PrometheusMetricsSource{}
}

// Collect fetches Prometheus text metrics from the configured endpoint, with retries and optional
// multi-scrape. If the endpoint is unreachable after the isUp check, collection is skipped without
// failing the bundle.
func (s *PrometheusMetricsSource) Collect(opts bundle.BundleOptions) ([]bundle.Artifact, error) {
	if !opts.Prometheus.Enabled {
		return nil, nil
	}
	endpoint := opts.Prometheus.MetricsEndpoint
	if endpoint == "" {
		endpoint = defaultPrometheusEndpoint
	}
	metricsURL := normalizeMetricsURL(endpoint)

	upCheckAttempts := upCheckMaxAttempts
	if opts.Prometheus.UpCheckMaxAttempts > 0 {
		upCheckAttempts = opts.Prometheus.UpCheckMaxAttempts
	}
	upCheckTimeoutVal := upCheckTimeout
	if opts.Prometheus.UpCheckTimeout > 0 {
		upCheckTimeoutVal = opts.Prometheus.UpCheckTimeout
	}
	upCheckDelayVal := upCheckDelayBetweenAttempts
	if opts.Prometheus.UpCheckDelay > 0 {
		upCheckDelayVal = opts.Prometheus.UpCheckDelay
	}

	plog("isup_start", "url", metricsURL, "attempts", strconv.Itoa(upCheckAttempts), "timeout", upCheckTimeoutVal.String())
	up, lastErr := s.checkEndpointUp(metricsURL, upCheckAttempts, upCheckTimeoutVal, upCheckDelayVal)
	if !up {
		plog("isup_skip", "url", metricsURL, "attempts", strconv.Itoa(upCheckAttempts), "error", lastErr.Error(), "outcome", "skipping_metrics_bundle_not_failed")
		msg := fmt.Sprintf("Prometheus metrics endpoint was not reachable after %d attempts.\nLast error: %v\nSkipped at: %s\n",
			upCheckAttempts, lastErr, time.Now().Format(time.RFC3339))
		return []bundle.Artifact{
			{
				Name:        "collector/metrics/prometheus_unavailable.txt",
				Data:        []byte(msg),
				Type:        "collector",
				CollectedAt: time.Now(),
			},
		}, nil
	}

	timeout := opts.Prometheus.ScrapeTimeout
	if timeout <= 0 {
		timeout = defaultScrapeTimeout
	}
	backoffInitial := opts.Prometheus.RetryBackoffInitial
	if backoffInitial <= 0 {
		backoffInitial = defaultRetryBackoffInitial
	}
	backoffMult := opts.Prometheus.RetryBackoffMultiplier
	if backoffMult <= 0 {
		backoffMult = defaultRetryBackoffMult
	}
	maxRetryDur := opts.Prometheus.MaxRetryDuration
	if maxRetryDur <= 0 {
		maxRetryDur = defaultMaxRetryDuration
	}

	plog("scrape_start", "url", metricsURL, "timeout", timeout.String(), "max_retry_duration", maxRetryDur.String())
	interval := opts.Prometheus.ScrapeInterval
	if interval > 0 {
		nScrapes := defaultMaxScrapes
		if opts.Prometheus.ScrapeDuration > 0 {
			nScrapes = int(opts.Prometheus.ScrapeDuration / interval)
			if nScrapes < 1 {
				nScrapes = 1
			}
			if nScrapes > maxScrapesCap {
				nScrapes = maxScrapesCap
			}
		}
		return s.collectMultiScrape(metricsURL, timeout, interval, nScrapes, backoffInitial, backoffMult, maxRetryDur)
	}
	return s.collectSingleScrape(metricsURL, timeout, backoffInitial, backoffMult, maxRetryDur)
}

func (s *PrometheusMetricsSource) checkEndpointUp(metricsURL string, maxAttempts int, attemptTimeout, delayBetween time.Duration) (bool, error) {
	client := &http.Client{Timeout: attemptTimeout}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := client.Get(metricsURL) //#nosec G107 -- URL is derived from operator-controlled config
		if err != nil {
			lastErr = err
			plog("isup_attempt", "attempt", strconv.Itoa(attempt), "max_attempts", strconv.Itoa(maxAttempts), "success", "false", "error", err.Error())
			if attempt < maxAttempts {
				time.Sleep(delayBetween)
			}
			continue
		}
		_ = resp.Body.Close()
		plog("isup_attempt", "attempt", strconv.Itoa(attempt), "max_attempts", strconv.Itoa(maxAttempts), "success", "true", "status", strconv.Itoa(resp.StatusCode))
		return true, nil
	}
	return false, lastErr
}

func normalizeMetricsURL(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return defaultPrometheusEndpoint + "/metrics"
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	if !strings.HasSuffix(endpoint, "/metrics") {
		if !strings.HasSuffix(endpoint, "/") {
			endpoint += "/"
		}
		endpoint += "metrics"
	}
	return endpoint
}

func (s *PrometheusMetricsSource) doScrapeWithRetry(url string, timeout, backoffInitial time.Duration, backoffMult float64, maxRetryDur time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	start := time.Now()
	attempt := 0
	var lastErr error
	for {
		attempt++
		elapsed := time.Since(start)
		plog("scrape_attempt", "attempt", strconv.Itoa(attempt), "elapsed_ms", strconv.FormatInt(elapsed.Milliseconds(), 10))
		resp, err := client.Get(url) //#nosec G107 -- URL is derived from operator-controlled config
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var buf bytes.Buffer
			_, err = buf.ReadFrom(resp.Body)
			resp.Body.Close()
			if err != nil {
				lastErr = fmt.Errorf("read body: %w", err)
				plog("scrape_error", "attempt", strconv.Itoa(attempt), "error", lastErr.Error(), "phase", "read_body")
			} else {
				sz := buf.Len()
				plog("scrape_success", "attempt", strconv.Itoa(attempt), "bytes", strconv.Itoa(sz), "duration_ms", strconv.FormatInt(elapsed.Milliseconds(), 10))
				return buf.Bytes(), nil
			}
		} else {
			if err != nil {
				lastErr = fmt.Errorf("get %s: %w", url, err)
			} else {
				resp.Body.Close()
				lastErr = fmt.Errorf("get %s: status %d", url, resp.StatusCode)
			}
			plog("scrape_attempt_failed", "attempt", strconv.Itoa(attempt), "error", lastErr.Error())
		}

		elapsed = time.Since(start)
		if elapsed >= maxRetryDur {
			plog("scrape_give_up", "reason", "max_retry_duration", "elapsed_ms", strconv.FormatInt(elapsed.Milliseconds(), 10), "error", lastErr.Error())
			return nil, fmt.Errorf("prometheus scrape failed after %v (max retry duration): %w", elapsed, lastErr)
		}

		backoff := backoffInitial
		for i := 0; i < attempt-1; i++ {
			backoff = time.Duration(float64(backoff) * backoffMult)
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
				break
			}
		}
		if elapsed+backoff > maxRetryDur {
			backoff = maxRetryDur - elapsed
			if backoff < time.Second {
				plog("scrape_give_up", "reason", "no_time_left", "elapsed_ms", strconv.FormatInt(elapsed.Milliseconds(), 10), "error", lastErr.Error())
				return nil, fmt.Errorf("prometheus scrape failed after %v: %w", elapsed, lastErr)
			}
		}
		plog("scrape_retry", "retry_in_ms", strconv.FormatInt(backoff.Milliseconds(), 10))
		time.Sleep(backoff)
	}
}

func (s *PrometheusMetricsSource) collectSingleScrape(metricsURL string, timeout, backoffInitial time.Duration, backoffMult float64, maxRetryDur time.Duration) ([]bundle.Artifact, error) {
	data, err := s.doScrapeWithRetry(metricsURL, timeout, backoffInitial, backoffMult, maxRetryDur)
	if err != nil {
		return nil, err
	}
	collectedAt := time.Now()
	header := fmt.Sprintf("# bindplane-support prometheus scrape source=%s scraped_at=%s\n", metricsURL, collectedAt.Format(time.RFC3339))
	txtData := make([]byte, 0, len(header)+len(data))
	txtData = append(txtData, header...)
	txtData = append(txtData, data...)
	env := prometheusArtifact{
		Source:    metricsURL,
		ScrapedAt: collectedAt.Format(time.RFC3339),
		Format:    "prometheus_exposition",
		Metrics:   string(data),
	}
	jsonData, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal prometheus json: %w", err)
	}
	return []bundle.Artifact{
		{Name: "collector/metrics/prometheus.txt", Data: txtData, Type: "collector", CollectedAt: collectedAt},
		{Name: "collector/metrics/prometheus.json", Data: jsonData, Type: "collector", CollectedAt: collectedAt},
	}, nil
}

func (s *PrometheusMetricsSource) collectMultiScrape(metricsURL string, timeout, interval time.Duration, nScrapes int, backoffInitial time.Duration, backoffMult float64, maxRetryDur time.Duration) ([]bundle.Artifact, error) {
	var txtBuf bytes.Buffer
	txtBuf.WriteString(fmt.Sprintf("# bindplane-support prometheus multi-scrape source=%s scrapes=%d interval=%s\n", metricsURL, nScrapes, interval))
	plog("multi_scrape_start", "scrapes", strconv.Itoa(nScrapes), "interval", interval.String())
	for i := 0; i < nScrapes; i++ {
		if i > 0 {
			plog("multi_scrape_round", "round", strconv.Itoa(i+1), "total", strconv.Itoa(nScrapes))
		}
		data, err := s.doScrapeWithRetry(metricsURL, timeout, backoffInitial, backoffMult, maxRetryDur)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			txtBuf.WriteString("\n")
		}
		txtBuf.WriteString(fmt.Sprintf("# scrape_at=%s\n", time.Now().Format(time.RFC3339)))
		txtBuf.Write(data)
		if i < nScrapes-1 {
			time.Sleep(interval)
		}
	}
	collectedAt := time.Now()
	fullText := txtBuf.String()
	env := prometheusArtifact{
		Source:    metricsURL,
		ScrapedAt: collectedAt.Format(time.RFC3339),
		Format:    "prometheus_exposition",
		Metrics:   fullText,
	}
	jsonData, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal prometheus json: %w", err)
	}
	return []bundle.Artifact{
		{Name: "collector/metrics/prometheus.txt", Data: []byte(fullText), Type: "collector", CollectedAt: collectedAt},
		{Name: "collector/metrics/prometheus.json", Data: jsonData, Type: "collector", CollectedAt: collectedAt},
	}, nil
}
