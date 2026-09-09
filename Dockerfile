# notiongate — build stage
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/notiongate ./cmd/notiongate

# runtime stage
FROM alpine:3.20
RUN adduser -D -u 10001 gate && mkdir -p /data && chown gate /data
COPY --from=build /out/notiongate /usr/local/bin/notiongate
USER gate
WORKDIR /data
ENV NOTIONGATE_HOST=0.0.0.0 \
    NOTIONGATE_PORT=8787 \
    DB_PATH=/data/notiongate.db
EXPOSE 8787
HEALTHCHECK --interval=30s --timeout=5s CMD wget -qO- http://127.0.0.1:8787/healthz || exit 1
ENTRYPOINT ["notiongate"]
CMD ["serve"]
