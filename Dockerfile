# The remote Fabrica server, as a small container.
#
#   docker build -t fabrica .
#   docker run -e FABRICA_SERVER_SECRET=… -v fabrica-data:/data -p 8080:8080 \
#     fabrica mcp --http --home https://github.com/acme/home.git
#
# The run-history database and the git cache live in /data, so mount a
# persistent volume there.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/staltz/fabrica/internal/cli.version=${VERSION}" \
      -o /fabrica ./cmd/fabrica

FROM alpine:3.22
# Fabrica runs git to read home repos and blueprints.
RUN apk add --no-cache git ca-certificates openssh-client \
 && adduser -D -u 10001 fabrica \
 && mkdir /data && chown fabrica /data
COPY --from=build /fabrica /usr/local/bin/fabrica
USER fabrica
WORKDIR /data
ENV FABRICA_CACHE=/data/cache
EXPOSE 8080
ENTRYPOINT ["fabrica"]
CMD ["mcp", "--http", "--db", "/data/runs.db"]
