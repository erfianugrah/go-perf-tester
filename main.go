package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net" // Required for custom dialer in Transport
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"
)

// TimingOutput holds the metrics for a single HTTP request.
type TimingOutput struct {
	RequestID int `json:"request_id"`

	DNSLookupDurationMs    float64 `json:"dns_lookup_duration_ms,omitempty"`
	TCPConnectDurationMs   float64 `json:"tcp_connect_duration_ms,omitempty"`
	TLSHandshakeDurationMs float64 `json:"tls_handshake_duration_ms,omitempty"`
	ServerProcessingMs     float64 `json:"server_processing_ms,omitempty"` // Time from WroteRequest to GotFirstResponseByte
	ContentTransferMs      float64 `json:"content_transfer_ms,omitempty"`  // Time from GotFirstResponseByte to end of body read (approx)

	OverallTotalMs float64 `json:"overall_total_ms"`

	HTTPStatus    int    `json:"http_code"`
	EffectiveURL  string `json:"url_effective"`
	ContentLength int64  `json:"size_download_bytes"`
	Error         string `json:"error,omitempty"`
}

// requestStat holds data from each request needed for the summary.
type requestStat struct {
	OverallTotalMs float64
	HTTPStatus     int
	Error          bool
}

var (
	// Command line flags
	concurrencyFlag = flag.Int("c", 1, "Number of concurrent requests (workers)")
	requestsFlag    = flag.Int("n", 1, "Total number of requests to make")
	quietFlag       = flag.Bool("q", false, "Suppress informational messages to stderr (summary still prints if enabled)")
	noSummaryFlag   = flag.Bool("no-summary", false, "Disable summary report on exit")
	targetURLStr    string // Positional argument
)

// logf prints to stderr if not in quiet mode.
func logf(format string, v ...interface{}) {
	if !*quietFlag {
		log.Printf(format, v...)
	}
}

// logToStderr always prints to stderr, used for summary or critical errors.
func logToStderr(format string, v ...interface{}) {
	log.Printf(format, v...)
}

// fatalf prints to stderr and exits.
func fatalf(format string, v ...interface{}) {
	log.Printf("Error: "+format, v...)
	os.Exit(1)
}

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "gocurlt - Go HTTP Timing Tool\n\n")
		fmt.Fprintf(os.Stderr, "This tool makes HTTP GET requests to a specified URL and outputs detailed\n")
		fmt.Fprintf(os.Stderr, "timing information for each request. By default, timing data is collected\n")
		fmt.Fprintf(os.Stderr, "and output as a single JSON array to stdout at the end.\n")
		fmt.Fprintf(os.Stderr, "It supports concurrent requests and provides a summary of all requests\n")
		fmt.Fprintf(os.Stderr, "to stderr upon completion or interruption (Ctrl+C).\n\n")
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <url>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s https://www.google.com\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -n 10 -c 5 https://www.example.com > results.json\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -q --no-summary https://api.example.com/health > results.json\n", os.Args[0])
	}
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: URL is required.\n\n")
		flag.Usage()
		os.Exit(1)
	}
	targetURLStr = flag.Arg(0)
	if _, err := url.ParseRequestURI(targetURLStr); err != nil {
		fatalf("Invalid URL: %v", err)
	}

	summaryEnabled := !*noSummaryFlag

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	workerCtx, workerCancel := context.WithCancel(rootCtx)
	defer workerCancel()

	// Determine actual concurrency for transport settings
	effectiveConcurrency := *concurrencyFlag
	if effectiveConcurrency <= 0 {
		effectiveConcurrency = 1
	}
	// transportConcurrencySetting is used for MaxIdleConnsPerHost.
	// It shouldn't exceed the number of requests if n is small.
	transportConcurrencySetting := effectiveConcurrency
	if *requestsFlag > 0 && *requestsFlag < effectiveConcurrency {
		transportConcurrencySetting = *requestsFlag
	}

	sharedClient := &http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			return nil
		},
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second, // Connection timeout
				KeepAlive: 30 * time.Second, // Keep-alive period
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          transportConcurrencySetting + 20, // Total idle connections in the pool
			MaxIdleConnsPerHost:   transportConcurrencySetting,      // Max idle connections to keep for a single host
			IdleConnTimeout:       90 * time.Second,                 // How long an idle connection is kept
			TLSHandshakeTimeout:   10 * time.Second,                 // TLS handshake timeout
			ExpectContinueTimeout: 1 * time.Second,
			// MaxConnsPerHost: transportConcurrencySetting, // Max active connections to a single host; 0 means unlimited
		},
	}

	var wg sync.WaitGroup
	jobBufferSize := effectiveConcurrency
	if *requestsFlag > 0 && *requestsFlag < jobBufferSize {
		jobBufferSize = *requestsFlag
	}
	if jobBufferSize == 0 {
		jobBufferSize = 1
	}

	jobs := make(chan int, jobBufferSize)
	resultsChan := make(chan TimingOutput, *requestsFlag)

	allRequestOutputs := make([]TimingOutput, 0, *requestsFlag)
	collectedStats := make([]requestStat, 0, *requestsFlag)
	var outputsMutex sync.Mutex

	numWorkersToLaunch := effectiveConcurrency
	if *requestsFlag > 0 && *requestsFlag < numWorkersToLaunch {
		numWorkersToLaunch = *requestsFlag
	}
	if numWorkersToLaunch == 0 && *requestsFlag > 0 {
		numWorkersToLaunch = 1
	}

	for i := 0; i < numWorkersToLaunch; i++ {
		wg.Add(1)
		go worker(workerCtx, i, &wg, jobs, resultsChan, targetURLStr, sharedClient)
	}

	var collectorWg sync.WaitGroup
	collectorWg.Add(1)
	go func() {
		defer collectorWg.Done()
		for result := range resultsChan {
			outputsMutex.Lock()
			allRequestOutputs = append(allRequestOutputs, result)
			if summaryEnabled {
				collectedStats = append(collectedStats, requestStat{
					OverallTotalMs: result.OverallTotalMs,
					HTTPStatus:     result.HTTPStatus,
					Error:          result.Error != "",
				})
			}
			outputsMutex.Unlock()
		}
	}()

	if *requestsFlag > 0 {
		for i := 1; i <= *requestsFlag; i++ {
			select {
			case jobs <- i:
			case <-workerCtx.Done():
				logf("Context cancelled, stopping job dispatch.")
				goto endJobLoop
			}
		}
	}
endJobLoop:
	close(jobs)

	allWorkersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(allWorkersDone)
	}()

	select {
	case <-allWorkersDone:
		logf("All workers finished processing jobs.")
	case sig := <-sigs:
		logf("Signal %v received. Cancelling pending work...", sig)
		workerCancel()
		select {
		case <-allWorkersDone:
			logf("All workers finished after cancellation signal.")
		case <-time.After(5 * time.Second):
			logToStderr("Timeout waiting for workers to finish after cancellation.")
		}
	}

	close(resultsChan)
	collectorWg.Wait()

	outputsMutex.Lock()
	if len(allRequestOutputs) > 0 {
		finalJSONBytes, err := json.Marshal(allRequestOutputs)
		if err != nil {
			logToStderr("Error marshaling final JSON array: %v", err)
		} else {
			fmt.Println(string(finalJSONBytes))
		}
	} else {
		// If no requests were made (e.g. -n 0), or all failed before collection
		fmt.Println("[]")
	}

	if summaryEnabled {
		printSummary(collectedStats)
	}
	outputsMutex.Unlock()

	if workerCtx.Err() == context.Canceled {
		outputsMutex.Lock()
		numCollected := len(allRequestOutputs)
		outputsMutex.Unlock()
		if *requestsFlag > 0 && numCollected < *requestsFlag {
			logf("Summary and JSON output based on %d processed results out of %d intended requests due to cancellation.", numCollected, *requestsFlag)
		}
	}
}

func worker(ctx context.Context, workerID int, wg *sync.WaitGroup, jobs <-chan int, results chan<- TimingOutput, urlStr string, client *http.Client) {
	defer wg.Done()
	logf("Worker %d started", workerID)
	for {
		select {
		case jobID, ok := <-jobs:
			if !ok {
				logf("Worker %d: jobs channel closed, exiting.", workerID)
				return
			}
			select {
			case <-ctx.Done():
				logf("Worker %d: context cancelled before processing job %d.", workerID, jobID)
				results <- TimingOutput{RequestID: jobID, Error: "Cancelled before start", EffectiveURL: urlStr}
				return
			default:
			}

			logf("Worker %d: processing request %d for %s", workerID, jobID, urlStr)
			timingResult := performRequest(ctx, jobID, urlStr, client)
			results <- timingResult

		case <-ctx.Done():
			logf("Worker %d: context cancelled, exiting.", workerID)
			return
		}
	}
}

func performRequest(ctx context.Context, requestID int, urlStr string, client *http.Client) TimingOutput {
	var opStart, dnsStart, dnsDone, connectStart, connectDone,
		tlsStart, tlsDone, wroteRequest, firstByte, bodyReadDone time.Time

	trace := &httptrace.ClientTrace{
		DNSStart:             func(_ httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:              func(_ httptrace.DNSDoneInfo) { dnsDone = time.Now() },
		ConnectStart:         func(_, _ string) { connectStart = time.Now() },
		ConnectDone:          func(_, _ string, err error) { connectDone = time.Now() },
		TLSHandshakeStart:    func() { tlsStart = time.Now() },
		TLSHandshakeDone:     func(_ tls.ConnectionState, _ error) { tlsDone = time.Now() },
		WroteRequest:         func(_ httptrace.WroteRequestInfo) { wroteRequest = time.Now() },
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}

	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	// Corrected error handling for request creation
	if err != nil {
		return TimingOutput{RequestID: requestID, Error: fmt.Sprintf("Failed to create request: %v", err), EffectiveURL: urlStr}
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	opStart = time.Now()
	resp, err := client.Do(req)
	opEnd := time.Now()

	output := TimingOutput{
		RequestID:      requestID,
		OverallTotalMs: float64(opEnd.Sub(opStart)) / float64(time.Millisecond),
		EffectiveURL:   urlStr,
	}

	if resp != nil {
		output.EffectiveURL = resp.Request.URL.String()
		output.HTTPStatus = resp.StatusCode
		output.ContentLength = resp.ContentLength

		if resp.Body != nil {
			bodyBytes, readErr := io.ReadAll(resp.Body)
			bodyReadDone = time.Now()
			if readErr != nil && output.Error == "" {
				output.Error = fmt.Sprintf("Error reading body: %v", readErr)
			}
			if output.ContentLength == -1 || (output.ContentLength == 0 && len(bodyBytes) > 0) {
				output.ContentLength = int64(len(bodyBytes))
			}
			resp.Body.Close()
		} else {
			if !firstByte.IsZero() {
				bodyReadDone = firstByte
			} else if !opEnd.IsZero() {
				bodyReadDone = opEnd
			}
		}
	}

	// Corrected and clarified error assignment logic
	if err != nil { // This 'err' is from client.Do(req)
		// Prioritize context cancellation error message if present
		if ctxErr := ctx.Err(); ctxErr != nil {
			output.Error = fmt.Sprintf("Request context error: %v (original client.Do error: %v)", ctxErr, err)
		} else if output.Error == "" { // If no body read error or other error already set
			output.Error = fmt.Sprintf("Request failed: %v", err)
		} else { // An error (e.g. body read) was already set, append client.Do error if different
			output.Error = fmt.Sprintf("%s; client.Do error: %v", output.Error, err)
		}
	}

	if !dnsStart.IsZero() && !dnsDone.IsZero() {
		output.DNSLookupDurationMs = float64(dnsDone.Sub(dnsStart)) / float64(time.Millisecond)
	}
	if !connectStart.IsZero() && !connectDone.IsZero() {
		output.TCPConnectDurationMs = float64(connectDone.Sub(connectStart)) / float64(time.Millisecond)
	}
	if !tlsStart.IsZero() && !tlsDone.IsZero() {
		output.TLSHandshakeDurationMs = float64(tlsDone.Sub(tlsStart)) / float64(time.Millisecond)
	}
	if !wroteRequest.IsZero() && !firstByte.IsZero() {
		output.ServerProcessingMs = float64(firstByte.Sub(wroteRequest)) / float64(time.Millisecond)
	}
	if !firstByte.IsZero() && !bodyReadDone.IsZero() && firstByte.Before(bodyReadDone) && resp != nil && resp.Body != nil {
		output.ContentTransferMs = float64(bodyReadDone.Sub(firstByte)) / float64(time.Millisecond)
	}

	return output
}

func printSummary(stats []requestStat) {
	if len(stats) == 0 {
		logToStderr("No requests processed for summary.")
		return
	}

	logToStderr("\n--- Request Summary ---")
	logToStderr("Total Requests Accounted for in Summary: %d", len(stats))

	var successfulRequests []requestStat
	failedRequestCount := 0
	httpCodeCounts := make(map[int]int)

	for _, s := range stats {
		if s.Error {
			failedRequestCount++
		} else {
			successfulRequests = append(successfulRequests, s)
		}
		if s.HTTPStatus != 0 || s.Error {
			httpCodeCounts[s.HTTPStatus]++
		}
	}
	logToStderr("Successful Requests: %d", len(successfulRequests))
	logToStderr("Failed Requests: %d", failedRequestCount)

	if len(successfulRequests) > 0 {
		var sumTimeTotal float64
		allTimes := make([]float64, len(successfulRequests))
		for i, s := range successfulRequests {
			sumTimeTotal += s.OverallTotalMs
			allTimes[i] = s.OverallTotalMs
		}
		sort.Float64s(allTimes)

		minTimeTotal := allTimes[0]
		maxTimeTotal := allTimes[len(allTimes)-1]
		avgTimeTotal := sumTimeTotal / float64(len(successfulRequests))

		logToStderr("Overall Total Time (ms) for successful requests:")
		logToStderr("  Min: %.3f", minTimeTotal)
		logToStderr("  Max: %.3f", maxTimeTotal)
		logToStderr("  Avg: %.3f", avgTimeTotal)
	}

	if len(httpCodeCounts) > 0 {
		logToStderr("HTTP Status Codes (includes errors where code might be 0):")
		codes := make([]int, 0, len(httpCodeCounts))
		for code := range httpCodeCounts {
			codes = append(codes, code)
		}
		sort.Ints(codes)
		for _, code := range codes {
			logToStderr("  %d: %d", code, httpCodeCounts[code])
		}
	}
	logToStderr("-----------------------")
}
