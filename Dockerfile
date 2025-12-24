# Build
FROM golang:1.25.4-alpine AS build
WORKDIR /rss

# Copy sources
COPY go.mod go.sum .
COPY src/ src/

# Compile
RUN go build -o server ./src

# Run
FROM alpine:3.23.2
WORKDIR /rss

# Copy binary and static files
COPY --from=build /rss/server /rss/server
COPY static/ static/

CMD /rss/server
