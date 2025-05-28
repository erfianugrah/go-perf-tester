# gocurlt - Go HTTP Timing Tool

`gocurlt` is a command-line tool written in Go for making HTTP GET requests to one or more URLs, collecting detailed timing metrics, and optionally capturing specified response headers. It supports concurrent requests and outputs results in JSON format, along with a summary to stderr.

## Features

* **Detailed Timing Metrics:** Captures DNS lookup, TCP connect, TLS handshake, server processing, and content transfer times.
* **Concurrent Requests:** Ability to run multiple requests in parallel using goroutines.
* **Multiple URL Support:**
    * Specify URLs directly on the command line.
    * Load URLs from a file (one URL per line).
    * Read URLs from stdin.
* **Requests per URL:** Control how many times each URL is requested.
* **Response Header Capturing:** Specify a list of response headers to include in the output.
* **JSON Output:** Outputs detailed results for each request as a JSON array to stdout, suitable for piping to other tools or saving to a file.
* **Summary Report:** Prints a summary of all requests (min, max, average, percentiles, HTTP status code counts) to stderr.
* **Graceful Shutdown:** Handles `Ctrl+C` (SIGINT/SIGTERM) to cancel pending work and still provide partial results and summary.
* **Customizable Logging:** Quiet mode to suppress informational messages.

## Installation

To use `gocurlt`, you need to have Go installed on your system.

1.  **Build from Source:**
    If you have the source code (e.g., `main.go`):
    ```bash
    # Navigate to the directory containing main.go
    go build -o gocurlt main.go
    ```
    This will create an executable file named `gocurlt` in the current directory.

2.  **Place in PATH (Optional):**
    Move the `gocurlt` executable to a directory in your system's PATH (e.g., `/usr/local/bin` or `~/bin`) to make it accessible from anywhere.
    ```bash
    mv gocurlt /usr/local/bin/
    ```

## Usage

````

gocurlt [options] \<url1\> [url2 ...]
or: gocurlt [options] -L \<url\_file\>
or: gocurlt [options] -L -

````

### Options

| Flag          | Default | Description                                                                                                |
|---------------|---------|------------------------------------------------------------------------------------------------------------|
| `-c <num>`    | `1`     | Number of concurrent requests (workers).                                                                   |
| `-n <num>`    | `1`     | Number of requests to make **per URL**.                                                                    |
| `-q`          | `false` | Suppress informational messages to stderr (summary still prints if enabled).                               |
| `--no-summary`| `false` | Disable summary report on exit.                                                                            |
| `-L <path>`   | `""`    | File to read URLs from (one URL per line). Use `-` for stdin. Overrides positional URL arguments.          |
| `-H <headers>`| `""`    | Comma-separated list of response headers to capture (e.g., `'X-Cache,Content-Type,KV-Status'`). Case-sensitive. |

### Examples

1.  **Single request to one URL:**
    ```bash
    ./gocurlt [https://www.google.com](https://www.google.com)
    ```

2.  **10 requests per URL, 5 concurrent workers, for two specific URLs, save JSON to a file:**
    ```bash
    ./gocurlt -n 10 -c 5 [https://api.example.com/ping](https://api.example.com/ping) [https://data.example.com/status](https://data.example.com/status) > results.json
    ```

3.  **Read URLs from `urls.txt`, 3 requests per URL, capture `X-Cache` and `Server` headers:**
    ```bash
    ./gocurlt -L urls.txt -n 3 -H "X-Cache,Server"
    ```
    *(urls.txt content example:)*
    ```
    [https://example.com/page1](https://example.com/page1)
    # This is a comment, will be ignored
    [https://example.com/page2](https://example.com/page2)
    ```

4.  **Read URLs from stdin, 5 requests per URL, quiet mode, no summary:**
    ```bash
    cat list_of_urls.txt | ./gocurlt -L - -n 5 -q --no-summary
    ```

## Output Format

`gocurlt` outputs a JSON array to `stdout`. Each object in the array represents a single HTTP request and contains the following fields:

```json
[
  {
    "request_id": 1,
    "dns_lookup_duration_ms": 5.234567,
    "tcp_connect_duration_ms": 10.876543,
    "tls_handshake_duration_ms": 20.123456,
    "server_processing_ms": 50.765432,
    "content_transfer_ms": 15.987654,
    "overall_total_ms": 103.12345,
    "http_code": 200,
    "url_target": "[https://example.com/resource](https://example.com/resource)",
    "url_effective": "[https://example.com/resource](https://example.com/resource)",
    "size_download_bytes": 10240,
    "captured_headers": {
      "X-Cache": "HIT",
      "Content-Type": "application/json"
    },
    "error": ""
  }
]
````

**Note:** `omitempty` fields will be omitted if their values are zero. `captured_headers` will be omitted if `-H` was not used or specified headers were not found.

## Summary Report (stderr)

A summary report is printed to `stderr` by default (unless `--no-summary` is used). It includes:

  * Total requests processed.
  * Number of successful and failed requests.
  * For successful requests: Min, Max, Average overall time, and Percentiles.
  * Counts of HTTP status codes.

Example:

```
--- Request Summary ---
Total Requests Accounted for in Summary: 100
Successful Requests (no error flag): 98
Failed Requests (error flag set): 2
Overall Total Time (ms) for successful requests:
  Min: 50.123
  Max: 502.456
  Avg: 123.456
  p50 (Median): 110.000
  p75: 150.000
  p90: 200.000
  p99: 350.000
  p99.9: 450.000
  p99.99: 500.000
  p99.999: 502.456
HTTP Status Codes (includes errors where code might be 0):
  0: 2
  200: 95
  404: 3
-----------------------
```

## Building from Source

You need Go (version 1.18 or later recommended) installed.

```bash
# Navigate to the directory containing the .go file (e.g., main.go)
go build -o gocurlt main.go
```

This will produce an executable named `gocurlt`.

## Contributing

Feel free to fork the repository, make improvements, and submit pull requests. For major changes, please open an issue first to discuss what you would like to change.

## License

This project can be considered under the (MIT License)[./LICENSE].

