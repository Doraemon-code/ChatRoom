# syntax=docker/dockerfile:1.7
FROM node:24-alpine AS web-build
WORKDIR /build/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/ ./
RUN npm run build

FROM golang:1.24-alpine AS relay-build
WORKDIR /build/server
COPY server/go.mod ./
COPY server/vendor ./vendor
COPY server/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/chatroom ./cmd/chatroom
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/certgen ./cmd/certgen
RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM scratch AS relay
COPY --from=relay-build /out/chatroom /chatroom
COPY --from=relay-build --chown=65532:65532 /out/data /data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/chatroom"]

FROM nginx:1.28-alpine AS edge
RUN rm -rf /etc/nginx/conf.d /docker-entrypoint.d
COPY --from=web-build /build/web/dist /usr/share/nginx/html
COPY --from=relay-build /out/certgen /usr/local/bin/certgen
COPY deploy/nginx/nginx.conf /etc/nginx/nginx.conf
COPY deploy/nginx/entrypoint.sh /entrypoint.sh
RUN chmod 0555 /entrypoint.sh && chown -R nginx:nginx /usr/share/nginx/html
USER nginx
EXPOSE 8443
ENTRYPOINT ["/entrypoint.sh"]
