#!/bin/sh
set -eu

umask 077
if [ -f /certs/tls.crt ] && [ -f /certs/tls.key ]; then
  cp /certs/tls.crt /run/chat-tls/tls.crt
  cp /certs/tls.key /run/chat-tls/tls.key
else
  certgen \
    -hosts "${CERT_HOSTS:-chat.local,localhost,127.0.0.1}" \
    -cert /run/chat-tls/tls.crt \
    -key /run/chat-tls/tls.key
fi
exec nginx -g 'daemon off;'
