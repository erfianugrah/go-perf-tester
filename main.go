package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// TimingOutput holds the metrics for a single HTTP request.
type TimingOutput struct {
	RequestID int `json:"request_id"` // Global unique request ID

	DNSLookupDurationMs    float64 `json:"dns_lookup_duration_ms,omitempty"`
	TCPConnectDurationMs   float64 `json:"tcp_connect_duration_ms,omitempty"`
	TLSHandshakeDurationMs float64 `json:"tls_handshake_duration_ms,omitempty"`
	ServerProcessingMs     float64 `json:"server_processing_ms,omitempty"` // Time from WroteRequest to GotFirstResponseByte
	ContentTransferMs      float64 `json:"content_transfer_ms,omitempty"`  // Time from GotFirstResponseByte to end of body read (approx)

	OverallTotalMs float64 `json:"overall_total_ms"`

	HTTPStatus      int               `json:"http_code"`
	TargetURL       string            `json:"url_target"`    // The URL that was intended to be fetched
	EffectiveURL    string            `json:"url_effective"` // The final URL after any redirects
	ContentLength   int64             `json:"size_download_bytes"`
	CapturedHeaders map[string]string `json:"captured_headers,omitempty"` // For storing captured response headers
	Error           string            `json:"error,omitempty"`
}

// requestStat holds data from each request needed for the summary.
type requestStat struct {
	OverallTotalMs float64
	HTTPStatus     int
	Error          bool
}

// Job defines a single request task for a worker.
type Job struct {
	ID        int    // Global request ID
	TargetURL string // The URL to fetch for this job
}

var (
	// Command line flags
	concurrencyFlag        = flag.Int("c", 1, "Number of concurrent requests (workers)")
	requestsFlag           = flag.Int("n", 1, "Number of requests to make per URL") // UPDATED Description
	quietFlag              = flag.Bool("q", false, "Suppress informational messages to stderr (summary still prints if enabled)")
	noSummaryFlag          = flag.Bool("no-summary", false, "Disable summary report on exit")
	urlListFileFlag        = flag.String("L", "", "File to read URLs from (one URL per line). Use '-' for stdin. Overrides positional URL arguments.")
	capturedHeadersStrFlag = flag.String("H", "", "Comma-separated list of response headers to capture (e.g., 'X-Cache,Content-Type,KV-Status')")
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

// readURLs loads URLs from a file, stdin, or command line arguments.
func readURLs() []string {
	var urls []string
	var err error
	var sourceName = "command line arguments"

	if *urlListFileFlag != "" {
		var reader io.Reader
		if *urlListFileFlag == "-" {
			reader = os.Stdin
			sourceName = "stdin"
			logf("Reading URLs from stdin (Ctrl+D to end)...")
		} else {
			file, ferr := os.Open(*urlListFileFlag)
			if ferr != nil {
				fatalf("Failed to open URL list file '%s': %v", *urlListFileFlag, ferr)
			}
			defer file.Close()
			reader = file
			sourceName = fmt.Sprintf("file '%s'", *urlListFileFlag)
		}
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" && !strings.HasPrefix(line, "#") { // Skip empty lines and comments
				urls = append(urls, line)
			}
		}
		err = scanner.Err()
	} else {
		urls = flag.Args()
		if len(urls) == 0 {
			fmt.Fprintf(os.Stderr, "Error: No URLs provided either as arguments or via -L flag.\n\n")
			flag.Usage()
			os.Exit(1)
		}
	}

	if err != nil {
		fatalf("Error reading URLs from %s: %v", sourceName, err)
	}

	if len(urls) == 0 {
		fatalf("No URLs found from %s.", sourceName)
	}

	var validURLs []string
	for _, u := range urls {
		if _, parseErr := url.ParseRequestURI(u); parseErr != nil {
			logToStderr("Warning: Invalid URL '%s' skipped: %v", u, parseErr)
		} else {
			validURLs = append(validURLs, u)
		}
	}

	if len(validURLs) == 0 {
		fatalf("No valid URLs to process after validation.")
	}
	logf("Loaded %d valid URLs to target from %s.", len(validURLs), sourceName)
	return validURLs
}

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "gocurlt - Go HTTP Timing Tool\n\n")
		fmt.Fprintf(os.Stderr, "This tool makes HTTP GET requests to specified URL(s) and outputs detailed\n")
		fmt.Fprintf(os.Stderr, "timing information for each request in JSON format to stdout.\n")
		fmt.Fprintf(os.Stderr, "It supports concurrent requests and provides a summary to stderr.\n\n")
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <url1> [url2 ...]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "   or: %s [options] -L <url_file>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "   or: %s [options] -L -\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults() // This will now print the updated description for -n
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s https://www.google.com\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -n 10 -c 5 https://api.example.com/ping https://data.example.com/status > results.json\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -L urls.txt -n 3 -H \"X-Cache,Server\" > results.json\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  cat list_of_urls.txt | %s -L - -n 5 -q --no-summary\n", os.Args[0])
	}
	flag.Parse()

	targetURLs := readURLs()
	numURLs := len(targetURLs)
	requestsPerURL := *requestsFlag

	if requestsPerURL <= 0 {
		fatalf("Number of requests per URL (-n) must be greater than 0.")
	}

	totalActualJobs := numURLs * requestsPerURL

	summaryEnabled := !*noSummaryFlag

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	workerCtx, workerCancel := context.WithCancel(rootCtx)
	defer workerCancel()

	effectiveConcurrency := *concurrencyFlag
	if effectiveConcurrency <= 0 {
		effectiveConcurrency = 1
	}

	transportConcurrencySetting := effectiveConcurrency
	if totalActualJobs < effectiveConcurrency && totalActualJobs > 0 {
		transportConcurrencySetting = totalActualJobs
	} else if totalActualJobs == 0 {
		transportConcurrencySetting = 1
	}
	// If totalActualJobs >= effectiveConcurrency, transportConcurrencySetting remains effectiveConcurrency

	sharedClient := &http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			return nil
		},
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          transportConcurrencySetting + 20,
			MaxIdleConnsPerHost:   transportConcurrencySetting,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}

	var wg sync.WaitGroup
	jobBufferSize := effectiveConcurrency
	if totalActualJobs < jobBufferSize && totalActualJobs > 0 {
		jobBufferSize = totalActualJobs
	} else if totalActualJobs == 0 {
		jobBufferSize = 1
	}
	// If totalActualJobs >= jobBufferSize, jobBufferSize remains effectiveConcurrency

	jobs := make(chan Job, jobBufferSize)

	resultsChanBufferSize := totalActualJobs
	if totalActualJobs > 0 && resultsChanBufferSize < effectiveConcurrency {
		resultsChanBufferSize = effectiveConcurrency
	} else if totalActualJobs == 0 {
		resultsChanBufferSize = 1
	}

	resultsChan := make(chan TimingOutput, resultsChanBufferSize)

	allRequestOutputs := make([]TimingOutput, 0, totalActualJobs)
	collectedStats := make([]requestStat, 0, totalActualJobs)
	var outputsMutex sync.Mutex

	numWorkersToLaunch := effectiveConcurrency
	if totalActualJobs < numWorkersToLaunch && totalActualJobs > 0 {
		numWorkersToLaunch = totalActualJobs
	} else if totalActualJobs == 0 {
		numWorkersToLaunch = 0
	}
	// If totalActualJobs >= numWorkersToLaunch, numWorkersToLaunch remains effectiveConcurrency

	for i := 0; i < numWorkersToLaunch; i++ {
		wg.Add(1)
		go worker(workerCtx, i, &wg, jobs, resultsChan, sharedClient)
	}

	var collectorWg sync.WaitGroup
	if totalActualJobs > 0 {
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
	}

	if totalActualJobs > 0 {
		logf("Dispatching %d total requests (%d per URL for %d URL(s))...", totalActualJobs, requestsPerURL, numURLs)
		globalRequestCounter := 0
		for _, currentURL := range targetURLs {
			for i := 1; i <= requestsPerURL; i++ {
				globalRequestCounter++
				currentJob := Job{
					ID:        globalRequestCounter,
					TargetURL: currentURL,
				}
				select {
				case jobs <- currentJob:
				case <-workerCtx.Done():
					logf("Context cancelled during job dispatch (Job ID %d for URL %s). Stopping dispatch.", globalRequestCounter, currentURL)
					goto endJobLoop
				}
			}
		}
	} else {
		logf("No jobs to dispatch (0 URLs provided/valid or -n is misconfigured).")
	}
endJobLoop:
	close(jobs)

	allWorkersDone := make(chan struct{})
	if numWorkersToLaunch > 0 {
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
	} else {
		close(allWorkersDone)
		logf("No workers were launched as there were no jobs to process.")
	}

	close(resultsChan)
	if totalActualJobs > 0 {
		collectorWg.Wait()
	}

	outputsMutex.Lock()
	if len(allRequestOutputs) > 0 {
		sort.Slice(allRequestOutputs, func(i, j int) bool {
			return allRequestOutputs[i].RequestID < allRequestOutputs[j].RequestID
		})
		finalJSONBytes, err := json.Marshal(allRequestOutputs)
		if err != nil {
			logToStderr("Error marshaling final JSON array: %v", err)
		} else {
			fmt.Println(string(finalJSONBytes))
		}
	} else {
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
		if totalActualJobs > 0 && numCollected < totalActualJobs {
			logf("Summary and JSON output based on %d processed results out of %d intended requests due to cancellation.", numCollected, totalActualJobs)
		}
	}
}

func worker(ctx context.Context, workerID int, wg *sync.WaitGroup, jobs <-chan Job, results chan<- TimingOutput, client *http.Client) {
	defer wg.Done()
	logf("Worker %d started", workerID)
	for {
		select {
		case job, ok := <-jobs:
			if !ok {
				logf("Worker %d: jobs channel closed, exiting.", workerID)
				return
			}
			select {
			case <-ctx.Done():
				logf("Worker %d: context cancelled before processing job %d (URL: %s).", workerID, job.ID, job.TargetURL)
				results <- TimingOutput{RequestID: job.ID, TargetURL: job.TargetURL, Error: "Cancelled before start"}
				return
			default:
			}

			logf("Worker %d: processing request %d for %s", workerID, job.ID, job.TargetURL)
			timingResult := performRequest(ctx, job.ID, job.TargetURL, client)
			results <- timingResult

		case <-ctx.Done():
			logf("Worker %d: context cancelled while waiting for job, exiting.", workerID)
			return
		}
	}
}

func performRequest(ctx context.Context, requestID int, targetURLStr string, client *http.Client) TimingOutput {
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

	req, err := http.NewRequestWithContext(ctx, "GET", targetURLStr, nil)
	if err != nil {
		return TimingOutput{RequestID: requestID, Error: fmt.Sprintf("Failed to create request: %v", err), TargetURL: targetURLStr, EffectiveURL: targetURLStr}
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	opStart = time.Now()
	resp, err := client.Do(req)
	opEnd := time.Now()

	output := TimingOutput{
		RequestID:      requestID,
		OverallTotalMs: float64(opEnd.Sub(opStart)) / float64(time.Millisecond),
		TargetURL:      targetURLStr,
		EffectiveURL:   targetURLStr,
	}

	if resp != nil {
		output.EffectiveURL = resp.Request.URL.String()
		output.HTTPStatus = resp.StatusCode
		output.ContentLength = resp.ContentLength

		if *capturedHeadersStrFlag != "" && resp.Header != nil {
			output.CapturedHeaders = make(map[string]string)
			desiredHeaders := strings.Split(*capturedHeadersStrFlag, ",")
			for _, headerName := range desiredHeaders {
				trimmedName := strings.TrimSpace(headerName)
				if val := resp.Header.Get(trimmedName); val != "" {
					output.CapturedHeaders[trimmedName] = val
				}
			}
		}

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

	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			output.Error = fmt.Sprintf("Request context error: %v (original client.Do error: %v)", ctxErr, err)
		} else if output.Error == "" {
			output.Error = fmt.Sprintf("Request failed: %v", err)
		} else {
			output.Error = fmt.Sprintf("%s; client.Do error: %v", output.Error, err)
		}
	}

	if !dnsStart.IsZero() && !dnsDone.IsZero() {
		output.DNSLookupDurationMs = float64(dnsDone.Sub(dnsStart)) / float64(time.Millisecond)
	}
	if !connectStart.IsZero() && !connectDone.IsZero() {
		tcpConnectEnd := connectDone
		if !tlsStart.IsZero() && tlsStart.After(connectStart) && (tlsStart.Before(connectDone) || tlsStart.Equal(connectDone)) {
			tcpConnectEnd = tlsStart
		}
		output.TCPConnectDurationMs = float64(tcpConnectEnd.Sub(connectStart)) / float64(time.Millisecond)
	}
	if !tlsStart.IsZero() && !tlsDone.IsZero() {
		output.TLSHandshakeDurationMs = float64(tlsDone.Sub(tlsStart)) / float64(time.Millisecond)
	}
	if !wroteRequest.IsZero() && !firstByte.IsZero() && firstByte.After(wroteRequest) {
		output.ServerProcessingMs = float64(firstByte.Sub(wroteRequest)) / float64(time.Millisecond)
	}
	if !firstByte.IsZero() && !bodyReadDone.IsZero() && bodyReadDone.After(firstByte) {
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
	logToStderr("Successful Requests (no error flag): %d", len(successfulRequests))
	logToStderr("Failed Requests (error flag set): %d", failedRequestCount)

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

		n := len(allTimes)
		if n > 0 {
			percentileIdx := func(p float64) int {
				if n == 0 {
					return 0
				}
				idx := int(math.Floor(p * float64(n-1)))
				if idx < 0 {
					return 0
				}
				if idx >= n {
					return n - 1
				}
				return idx
			}

			logToStderr("  p50 (Median): %.3f", allTimes[percentileIdx(0.50)])
			logToStderr("  p75: %.3f", allTimes[percentileIdx(0.75)])
			logToStderr("  p90: %.3f", allTimes[percentileIdx(0.90)])
			logToStderr("  p99: %.3f", allTimes[percentileIdx(0.99)])
			logToStderr("  p99.9: %.3f", allTimes[percentileIdx(0.999)])
			logToStderr("  p99.99: %.3f", allTimes[percentileIdx(0.9999)])
			logToStderr("  p99.999: %.3f", allTimes[percentileIdx(0.99999)])
		}
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
