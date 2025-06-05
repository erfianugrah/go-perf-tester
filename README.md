# Go Performance Tester (gocurlt)

A comprehensive Go-based HTTP performance testing tool that measures detailed timing metrics for HTTP requests with support for custom headers, range requests, and advanced testing scenarios.

## Features

- **Detailed Timing Metrics**: Captures DNS lookup, TCP connect, TLS handshake, server processing, and content transfer times
- **Concurrent Testing**: Supports multiple concurrent workers for load testing
- **Multiple URLs**: Test multiple URLs in a single run with configurable requests per URL
- **Custom Request Headers**: Send authentication, user-agent, and other custom headers
- **Range Request Testing**: Test partial content delivery and CDN behavior with byte ranges
- **JSON Output**: Structured output for easy parsing and analysis
- **Summary Statistics**: Comprehensive performance summary with percentiles
- **ASCII Graphs**: Visual representation of response time distribution
- **Response Headers**: Capture specific response headers for analysis
- **Flexible Input**: Read URLs from command line, files, or stdin
- **Error Handling**: Robust error handling with detailed error messages

## Installation

```bash
go build -o gocurlt main.go
```

## Usage

### Basic Usage

```bash
# Test a single URL
./gocurlt https://example.com

# Test multiple URLs with 10 requests each, 5 concurrent workers
./gocurlt -n 10 -c 5 https://api.example.com https://cdn.example.com

# Read URLs from a file
./gocurlt -L urls.txt -n 5

# Read URLs from stdin
cat urls.txt | ./gocurlt -L - -n 3
```

### Advanced Features

#### Custom Request Headers

Send custom headers like authentication tokens, user agents, or content type preferences:

```bash
# Send authorization header
./gocurlt -header "Authorization: Bearer your-token-here" https://api.example.com

# Multiple headers
./gocurlt -header "Authorization: Bearer token,User-Agent: MyBot/1.0,Accept: application/json" https://api.example.com

# Test API with custom headers across multiple requests
./gocurlt -n 50 -c 5 -header "X-API-Key: secret123,Content-Type: application/json" https://api.example.com
```

#### Range Request Testing

Test partial content delivery, CDN behavior, and resume capabilities:

```bash
# Test first 1KB of a file
./gocurlt -range "0-1023" https://example.com/largefile.zip

# Test second 1KB block
./gocurlt -range "1024-2047" https://example.com/largefile.zip

# Test suffix range (last 1KB)
./gocurlt -range "-1024" https://example.com/largefile.zip

# Test range requests across multiple URLs
./gocurlt -range "0-1023" -n 10 https://cdn1.example.com/file.zip https://cdn2.example.com/file.zip
```

#### Combined Testing Scenarios

```bash
# Test authenticated range requests
./gocurlt -header "Authorization: Bearer token" -range "0-1023" https://api.example.com/download

# Performance test with custom headers
./gocurlt -n 100 -c 10 -header "User-Agent: LoadTester/1.0" https://api.example.com

# Test same range across multiple CDN endpoints
./gocurlt -range "0-4095" -n 20 -c 5 https://cdn1.example.com/file.dat https://cdn2.example.com/file.dat
```

### Command Line Options

- `-c int`: Number of concurrent requests (workers) (default 1)
- `-n int`: Number of requests to make per URL (default 1)
- `-q`: Suppress informational messages to stderr
- `-no-summary`: Disable summary report on exit
- `-L string`: File to read URLs from (one URL per line). Use '-' for stdin
- `-H string`: Comma-separated list of response headers to capture
- `-header string`: Comma-separated list of request headers to send (e.g., 'Authorization: Bearer token,User-Agent: MyBot/1.0')
- `-range string`: Byte range to request (e.g., '0-1023' for first 1KB, '1024-2047' for second KB)
- `-graphs`: Show ASCII graphs in summary output (default true)

### URL Testing Patterns

Yes! The tool allows you to test the **same range multiple times across multiple URLs**:

```bash
# Test the same 1KB range 50 times across 3 different CDN endpoints
./gocurlt -range "0-1023" -n 50 -c 10 \
  https://cdn1.example.com/largefile.zip \
  https://cdn2.example.com/largefile.zip \
  https://cdn3.example.com/largefile.zip

# This will make:
# - 50 requests to cdn1.example.com/largefile.zip (bytes 0-1023)
# - 50 requests to cdn2.example.com/largefile.zip (bytes 0-1023)  
# - 50 requests to cdn3.example.com/largefile.zip (bytes 0-1023)
# Total: 150 requests testing the same range across different endpoints
```

### Comprehensive Examples

```bash
# Basic performance test
./gocurlt -n 100 -c 10 https://api.example.com > results.json

# API testing with authentication
./gocurlt -header "Authorization: Bearer $(cat token.txt)" -n 50 https://api.example.com

# CDN range request performance comparison
./gocurlt -range "0-1023" -n 25 -c 5 \
  https://us-east.cdn.example.com/file.zip \
  https://eu-west.cdn.example.com/file.zip \
  https://asia.cdn.example.com/file.zip

# Capture specific response headers
./gocurlt -H "X-Cache,Server,Content-Type" -n 10 https://example.com

# Load test with custom user agent
./gocurlt -header "User-Agent: LoadTester/1.0" -n 200 -c 20 https://api.example.com

# Test file resume capability
./gocurlt -range "1048576-2097151" -header "User-Agent: ResumeTest" https://downloads.example.com/largefile.bin

# Quiet mode with no summary for automated testing
./gocurlt -q -no-summary -range "0-512" https://example.com

# Test multiple URLs from file with custom headers
echo "https://api1.example.com" > endpoints.txt
echo "https://api2.example.com" >> endpoints.txt
./gocurlt -L endpoints.txt -n 30 -header "X-Test-Run: $(date +%s)"
```

## Output Format

The tool outputs JSON to stdout with detailed timing information for each request:

```json
[
  {
    "request_id": 1,
    "dns_lookup_duration_ms": 15.234,
    "tcp_connect_duration_ms": 25.567,
    "tls_handshake_duration_ms": 45.123,
    "server_processing_ms": 150.456,
    "content_transfer_ms": 5.789,
    "overall_total_ms": 241.169,
    "http_code": 206,
    "url_target": "https://example.com/file.zip",
    "url_effective": "https://example.com/file.zip",
    "size_download_bytes": 1024,
    "captured_headers": {
      "Server": "nginx/1.18.0",
      "X-Cache": "HIT",
      "Content-Range": "bytes 0-1023/1048576"
    }
  }
]
```

### Field Descriptions

- `request_id`: Unique identifier for each request
- `dns_lookup_duration_ms`: Time spent on DNS resolution
- `tcp_connect_duration_ms`: Time to establish TCP connection
- `tls_handshake_duration_ms`: Time for TLS/SSL handshake
- `server_processing_ms`: Time from request sent to first response byte
- `content_transfer_ms`: Time to download response body
- `overall_total_ms`: Total request time
- `http_code`: HTTP status code (200, 206 for partial content, etc.)
- `url_target`: Original requested URL
- `url_effective`: Final URL after redirects
- `size_download_bytes`: Number of bytes downloaded
- `captured_headers`: Response headers captured via `-H` flag

## Summary Statistics

When enabled (default), the tool prints a comprehensive performance summary to stderr including:

- Request success/failure rates
- Response time statistics (min, max, average)
- Percentile analysis (p50, p75, p90, p95, p99, etc.)
- ASCII histograms and distribution graphs
- HTTP status code breakdown (including 206 for range requests)

## Use Cases

### API Performance Testing
```bash
# Test API endpoint performance with authentication
./gocurlt -header "Authorization: Bearer $API_TOKEN" -n 100 -c 10 https://api.example.com/v1/data
```

### CDN Performance Comparison
```bash
# Compare the same file across multiple CDN endpoints
./gocurlt -range "0-4095" -n 50 -c 5 \
  https://cdn1.example.com/assets/app.js \
  https://cdn2.example.com/assets/app.js \
  https://cdn3.example.com/assets/app.js
```

### Download Resume Testing
```bash
# Test if server supports range requests properly
./gocurlt -range "1024-2047" https://downloads.example.com/installer.exe
```

### Load Testing with Custom Headers
```bash
# Simulate mobile app traffic
./gocurlt -header "User-Agent: MobileApp/2.1 (iOS 15.0)" -n 200 -c 15 https://mobile-api.example.com
```

## Error Handling

The tool provides clear error messages for common issues:

- Invalid header format: `Error parsing request headers: invalid header format 'invalid' - expected 'Name: Value'`
- Invalid range format: `Error parsing range header: invalid range format 'invalid' - expected format like '0-1023', '1024-', or '-1024'`
- Network errors: Captured in the JSON output's `error` field

## License

MIT License - see LICENSE file for details.