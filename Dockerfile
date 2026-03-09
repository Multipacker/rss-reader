# Build
FROM golang:1.25.4-alpine3.22 AS build
WORKDIR /rss

# Copy sources
COPY go.mod backend.go ./
COPY static/ static/

# Compile
RUN go build -o server .

# Run
FROM alpine:3.23.2
WORKDIR /rss

# Copy binary and static files
COPY --from=build /rss/server /rss/server

CMD /rss/server
