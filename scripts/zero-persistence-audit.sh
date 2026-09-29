#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
scope="web/src server/cmd/chatroom server/internal Dockerfile docker-compose.yml"

forbidden='localStorage|sessionStorage|indexedDB|document\.cookie|navigator\.serviceWorker|(^|[^[:alnum:]_])caches\.|req\.ip|RemoteAddr|X-Forwarded-For|CF-Connecting-IP|X-Real-IP|User-Agent|console\.(log|error)|Sentry|Google Analytics|Analytics|sqlite|postgres|mysql|mongo|redis|Kafka|RabbitMQ'
if rg -n -i -g '!*.test.ts' -g '!*_test.go' "$forbidden" $scope; then
  echo "FAIL: prohibited persistence, IP, telemetry, or production console token found"
  exit 1
fi

if rg -n -g '!*.test.ts' -g '!*_test.go' 'log\.(Print|Printf|Println|Fatal|Panic)' server/cmd/chatroom server/internal | rg -v 'log\.Print\("(server_started|server_stopped|fatal:listen_failed|fatal:forum_store)"\)'; then
	echo "FAIL: non-allowlisted application log found"
	exit 1
fi

test "$(rg -n 'proxy_set_header (User-Agent|X-Forwarded-For|X-Real-IP|Forwarded) ""' deploy/nginx/nginx.conf | wc -l)" -eq 4
test "$(rg -n 'access_log off' deploy/nginx/nginx.conf | wc -l)" -eq 1
test "$(rg -n 'driver: none' docker-compose.yml | wc -l)" -eq 2
test "$(rg -n 'Cache-Control.*no-store' deploy/nginx/nginx.conf | wc -l)" -ge 1

echo "PASS: browser chat storage, IP access, telemetry, and sensitive log patterns absent"
echo "PASS: forum persistence is limited to names/messages/replies; no IP, MAC, time, or request metadata fields"
echo "PASS: Nginx access logs and Docker container logs disabled"
echo "NOTE: certgen writes only a TLS certificate and key to /run/chat-tls tmpfs; it has no chat data input"
