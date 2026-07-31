# NOTE: RUN "just --list" TO VIEW ALL COMMANDS

set dotenv-load

# build binary file
[group('build')]
build:
    @go build -o vuja ./cmd/vuja

# build, install, and configure an optional shell
[group('build')]
[linux]
[macos]
install $shell="":
    @go build -o vuja ./cmd/vuja
    @if [ -n "$shell" ]; then ./vuja setup "$shell"; else ./vuja setup; fi

# build with optimized binary file
[group('build')]
optimized-build:
    @GOAMD64=v4 go build -ldflags="-s -w" -trimpath -o vuja ./cmd/vuja

# build a versioned release binary
# usage: just build-release v1.2.0
[group('build')]
build-release version:
    @GOAMD64=v3 go build -pgo=auto -ldflags="-s -w -X github.com/faustbrian/vuja/root.Version={{ version }}" -trimpath -o vuja ./cmd/vuja

# run vuja
[group('dev')]
run:
    @./vuja

# initialize default config file
[group('dev')]
config-init:
    @./vuja config init

# re-build and reload vuja
[group('dev')]
[linux]
[macos]
reload:
    @go build -o vuja ./cmd/vuja
    @if [ -f ~/.local/bin/vuja ]; then rm -f ~/.local/bin/vuja && cp ./vuja ~/.local/bin/vuja; fi
    @if [ -n "${VUJA_PID:-}" ]; then kill -USR1 $VUJA_PID 2>/dev/null || true; fi
    @if [ -z "${VUJA_FD:-}" ]; then ./vuja; fi

# copy to local bin
[group('dev')]
copy:
    @rm ~/.local/bin/vuja
    @cp ./vuja ~/.local/bin/vuja

# run all tests
[group('test')]
test:
    @go test ./... -v

# report deterministic scoring, completion, history, prompt, and status benchmarks
[group('test')]
bench:
    @go test -run '^$' -bench 'Benchmark(ScoreCandidates|FileGeneratorPrefix|Search.*RichHistory.*10K)$' -benchmem -count=5 ./internal/scoring ./spec ./integration
    @go test -run '^$' -bench 'Benchmark(Chatbox|Prompt|Repository|Status|System)' -benchmem -count=5 ./root

# run project health and scoring analyzer
alias ana := analyze
[group('test')]
analyze:
    @go run scripts/test_analyzer.go

# run linter
[group('test')]
lint:
    @golangci-lint run ./...

# generate README docs for commands
[group('gen')]
gen-docs:
    @go run scripts/cmds_docs.go

# update Go modules
[group('pkg')]
pkg:
    @go mod tidy

# vuja debugger
[group('debug')]
debug:
    @./vuja --debug

# test the update command (version check + comparison), no full vuja session needed
# usage: just debug-update v1.99.0
[group('debug')]
debug-update version="v1.99.0":
    #!/bin/sh
    tmp=$(mktemp -d)
    printf '{"tag_name":"%s"}' "{{ version }}" > "$tmp/response.json"
    python3 -m http.server 19999 --directory "$tmp" 2>/dev/null &
    SERVER_PID=$!
    sleep 0.3
    echo "--- testing vuja update command ---"
    VUJA_UPDATE_URL="http://localhost:19999/response.json" ./vuja update
    kill $SERVER_PID 2>/dev/null
    rm -rf "$tmp"

# test the in-session update notification banner (requires vuja.zsh hook to be active)
# usage: just debug-notify v1.99.0
[group('debug')]
debug-notify version="v1.99.0":
    VUJA_PID="" VUJA_MOCK_LATEST_VERSION="{{ version }}" ./vuja

# test the install script locally
# usage: just debug-install v1.0.0
[group('debug')]
debug-install version="v1.0.0":
    #!/bin/sh
    PORT=19998
    TMP=$(mktemp -d)
    mkdir -p "$TMP/home"
    PASS=0
    FAIL=0

    # always cleanup server + tmp, even if a test crashes
    cleanup() {
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
        rm -rf "$TMP"
    }
    trap cleanup EXIT

    ok()   { PASS=$((PASS+1)); printf "  \033[32m✓\033[0m %s\n" "$1"; }
    fail() { FAIL=$((FAIL+1)); printf "  \033[31m✗\033[0m %s\n" "$1"; }

    # detect actual architecture
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    MACH=$(uname -m)
    case "$MACH" in
        x86_64|amd64)  ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *) echo "unsupported arch: $MACH"; exit 1 ;;
    esac
    ARCHIVE_NAME="vuja_${OS}_${ARCH}.tar.gz"

    echo "building vuja ({{ version }}, ${OS}_${ARCH})..."
    go build -ldflags="-X github.com/faustbrian/vuja/root.Version={{ version }}" -o "$TMP/vuja" ./cmd/vuja

    echo "packaging archive ($ARCHIVE_NAME)..."
    tar -czf "$TMP/$ARCHIVE_NAME" -C "$TMP" vuja
    if command -v sha256sum >/dev/null 2>&1; then
        (cd "$TMP" && sha256sum "$ARCHIVE_NAME" > SHA256SUMS)
    else
        (cd "$TMP" && shasum -a 256 "$ARCHIVE_NAME" > SHA256SUMS)
    fi
    printf '%064d  %s\n' 0 "$ARCHIVE_NAME" > "$TMP/BAD_SHA256SUMS"

    # start mock server
    python3 -c 'import sys, textwrap; exec(textwrap.dedent(r"""
    import os, sys
    from http.server import HTTPServer, BaseHTTPRequestHandler

    port = int(sys.argv[1])
    tmp_dir = sys.argv[2]
    version = sys.argv[3]
    os_name = sys.argv[4]
    arch = sys.argv[5]

    class mock_handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if "/repos/faustbrian/vuja/releases/latest" in self.path:
                if self.path.startswith("/404"):
                    self.send_response(404)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(b"{\"message\":\"Not Found\",\"documentation_url\":\"https://docs.github.com/rest\"}")
                elif self.path.startswith("/ratelimit"):
                    self.send_response(403)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(b"{\"message\":\"API rate limit exceeded for ...\",\"documentation_url\":\"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api\"}")
                else:
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    download_url = "http://localhost:" + str(port) + "/vuja_" + os_name + "_" + arch + ".tar.gz"
                    checksum_name = "BAD_SHA256SUMS" if self.path.startswith("/badchecksum") else "SHA256SUMS"
                    checksum_url = "http://localhost:" + str(port) + "/" + checksum_name
                    response = "{\n  \"tag_name\": \"" + version + "\",\n  \"assets\": [\n    {\n      \"name\": \"vuja_" + os_name + "_" + arch + ".tar.gz\",\n      \"browser_download_url\": \"" + download_url + "\"\n    },\n    {\n      \"name\": \"SHA256SUMS\",\n      \"browser_download_url\": \"" + checksum_url + "\"\n    }\n  ]\n}\n"
                    self.wfile.write(response.encode("utf-8"))
            elif self.path.endswith(".tar.gz") or self.path.endswith("SHA256SUMS"):
                filename = os.path.basename(self.path)
                filepath = os.path.join(tmp_dir, filename)
                if os.path.exists(filepath):
                    self.send_response(200)
                    self.send_header("Content-Type", "application/octet-stream")
                    self.send_header("Content-Length", str(os.path.getsize(filepath)))
                    self.end_headers()
                    with open(filepath, "rb") as f:
                        self.wfile.write(f.read())
                else:
                    self.send_response(404)
                    self.end_headers()
            else:
                self.send_response(200)
                self.end_headers()

        def log_message(self, *args):
            pass

    HTTPServer(("", port), mock_handler).serve_forever()
    """))' "$PORT" "$TMP" "{{ version }}" "$OS" "$ARCH" 2>/dev/null &
    SERVER_PID=$!

    # wait up to three seconds for the server
    i=0
    until curl -sf "http://localhost:${PORT}/" >/dev/null 2>&1; do
        i=$((i+1))
        [ $i -ge 30 ] && echo "mock server failed to start" && exit 1
        sleep 0.1
    done

    echo ""
    echo "--- running test cases ---"

    printf "test 1: happy path install... "
    OUT=$(HOME="$TMP/home" BIN_DIR="$TMP/out1" VUJA_API_URL="http://localhost:${PORT}/happy" sh scripts/install.sh 2>&1) && STATUS=0 || STATUS=$?
    if [ $STATUS -eq 0 ] && echo "$OUT" | grep -q "Checksum verified" && echo "$OUT" | grep -q "Installation verified"; then
        ok "checksum and installed binary verified"
    else
        fail "happy path failed\n$OUT"
    fi

    printf "test 2: version string matches... "
    BIN="$TMP/out1/vuja"
    if [ -x "$BIN" ]; then
        VER=$("$BIN" version 2>&1)
        if echo "$VER" | grep -q "{{ version }}"; then
            ok "got '$VER'"
        else
            fail "expected {{ version }}, got '$VER'"
        fi
    else
        fail "binary not found at $BIN"
    fi

    printf "test 3: 404 no release error... "
    OUT=$(HOME="$TMP/home" BIN_DIR="$TMP/out3" VUJA_API_URL="http://localhost:${PORT}/404" sh scripts/install.sh 2>&1) && STATUS=0 || STATUS=1
    if [ $STATUS -ne 0 ] && echo "$OUT" | grep -qi "no releases found\|not have published"; then
        ok "correct error message for 404"
    else
        fail "expected 404 error, got: $OUT"
    fi

    printf "test 4: rate limit error... "
    OUT=$(HOME="$TMP/home" BIN_DIR="$TMP/out4" VUJA_API_URL="http://localhost:${PORT}/ratelimit" sh scripts/install.sh 2>&1) && STATUS=0 || STATUS=1
    if [ $STATUS -ne 0 ] && echo "$OUT" | grep -qi "rate limit\|rate limited"; then
        ok "correct error message for rate limit"
    else
        fail "expected rate limit error, got: $OUT"
    fi

    printf "test 5: wget code path... "
    if ! command -v wget >/dev/null 2>&1; then
        ok "skipped (wget not installed)"
    else
        RELEASES=$(wget -qO- "http://localhost:${PORT}/happy/repos/faustbrian/vuja/releases/latest" 2>&1) && WS=0 || WS=$?
        URL=$(echo "$RELEASES" | grep "browser_download_url" | grep "${OS}_${ARCH}" | head -1 | cut -d '"' -f 4)
        if [ -n "$URL" ]; then
            ok "wget parses download URL correctly ($URL)"
        else
            fail "wget could not parse URL from response: $RELEASES"
        fi
    fi

    printf "test 6: checksum mismatch rejected... "
    OUT=$(HOME="$TMP/home" BIN_DIR="$TMP/out6" VUJA_API_URL="http://localhost:${PORT}/badchecksum" sh scripts/install.sh 2>&1) && STATUS=0 || STATUS=$?
    if [ $STATUS -ne 0 ] && echo "$OUT" | grep -qi "checksum mismatch"; then
        ok "corrupt release rejected"
    else
        fail "expected checksum error, got: $OUT"
    fi

    echo ""
    printf "results: \033[32m%d passed\033[0m, \033[31m%d failed\033[0m\n" "$PASS" "$FAIL"
    [ $FAIL -eq 0 ] || exit 1
